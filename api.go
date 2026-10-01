package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"
)

type App struct {
	auth    *Auth
	graphe  *Graphe
	nc      *Nextcloud
	store   *Store
	quitter func()
}

// f : fournisseur choisi dans les réglages.
func (app *App) f() Fournisseur {
	if app.auth.Config().Nextcloud() {
		return app.nc
	}
	return fournisseurMicrosoft{g: app.graphe, auth: app.auth}
}

func NouvelleApp(dir, redirect string) (*App, error) {
	a, err := NouvelleAuth(dir, redirect)
	if err != nil {
		return nil, err
	}
	app := &App{auth: a, graphe: NouveauGraphe(a), nc: NouveauNextcloud(a)}
	app.store = &Store{stock: stockageFournisseur{f: app.f}, qui: func() string {
		if c := a.Compte(); c != nil {
			return c.Nom
		}
		return ""
	}}
	return app, nil
}

func (app *App) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/connexion", app.connexion)
	mux.HandleFunc("GET /auth/retour", app.retour)

	api := func(motif string, h func(*http.Request) (any, error)) {
		mux.HandleFunc(motif, func(w http.ResponseWriter, r *http.Request) {
			// Les écritures viennent uniquement de notre page (pas d'un autre site).
			if r.Method != "GET" && r.Header.Get("X-Regie") != "1" {
				http.Error(w, "requête refusée", http.StatusForbidden)
				return
			}
			res, err := h(r)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			if err != nil {
				code := http.StatusBadRequest
				var eg *ErreurGraphe
				var en *ErreurNextcloud
				switch {
				case errors.As(err, &en) && en.Statut >= 500:
					code = http.StatusBadGateway
				case errors.Is(err, ErrNonConnecte):
					code = http.StatusUnauthorized
				case errors.Is(err, ErrIntrouvable):
					code = http.StatusNotFound
				case errors.As(err, &eg) && eg.Statut >= 500:
					code = http.StatusBadGateway
				}
				w.WriteHeader(code)
				_ = json.NewEncoder(w).Encode(map[string]string{"erreur": messageErreur(err)})
				return
			}
			if res == nil {
				res = map[string]bool{"ok": true}
			}
			_ = json.NewEncoder(w).Encode(res)
		})
	}

	api("GET /api/etat", app.etat)
	api("POST /api/config", app.config)
	api("POST /api/deconnexion", func(r *http.Request) (any, error) {
		if app.auth.Config().Nextcloud() {
			app.nc.Deconnecter(r.Context())
		}
		app.auth.Deconnecter()
		return nil, nil
	})
	api("POST /api/connexion/nextcloud", func(r *http.Request) (any, error) {
		u, err := app.nc.DemarrerConnexion(r.Context())
		return map[string]string{"url": u}, err
	})
	api("POST /api/quitter", func(*http.Request) (any, error) { app.quitter(); return nil, nil })

	api("GET /api/donnees", app.donnees)
	api("POST /api/regisseurs", app.enregistrerRegisseur)
	api("DELETE /api/regisseurs/{id}", app.supprimerRegisseur)
	api("POST /api/projets", app.enregistrerProjet)
	api("DELETE /api/projets/{id}", app.supprimerProjet)

	api("GET /api/projets/{id}/documents", app.documents)
	api("PUT /api/projets/{id}/documents", app.envoyerDocument)
	api("DELETE /api/projets/{id}/documents/{doc...}", app.supprimerDocument)

	api("GET /api/projets/{id}/message/{reg}", app.messageParDefaut)
	api("POST /api/projets/{id}/acces", app.donnerAcces)
	api("POST /api/projets/{id}/acces/{reg}/renvoyer", app.renvoyerAcces)
	api("DELETE /api/projets/{id}/acces/{reg}", app.retirerAcces)
}

func messageErreur(err error) string {
	var eg *ErreurGraphe
	if errors.As(err, &eg) {
		switch {
		case eg.Statut == 403 && strings.Contains(strings.ToLower(eg.Message), "shar"):
			return "Partage refusé par Microsoft 365 : le partage externe est probablement désactivé pour ce site (voir le guide administrateur)."
		case eg.Statut == 403:
			return "Accès refusé par Microsoft 365 : " + eg.Message
		case eg.Statut == 507:
			return "Espace de stockage Microsoft 365 plein."
		}
	}
	var en *ErreurNextcloud
	if errors.As(err, &en) && en.Statut == 403 && strings.Contains(strings.ToLower(en.Message), "shar") {
		return "Partage refusé par Nextcloud : " + en.Message + " (vérifier les réglages de partage du serveur)."
	}
	return err.Error()
}

func lireJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return errors.New("requête invalide")
	}
	return nil
}

// --- Connexion ---

func (app *App) etat(*http.Request) (any, error) {
	cfg := app.auth.Config()
	return map[string]any{
		"version":            version,
		"config":             cfg,
		"configComplete":     cfg.Complete(),
		"compte":             app.auth.Compte(),
		"connexionNextcloud": app.nc.EtatConnexion(),
	}, nil
}

func (app *App) config(r *http.Request) (any, error) {
	var c Config
	if err := lireJSON(r, &c); err != nil {
		return nil, err
	}
	return nil, app.auth.EnregistrerConfig(c)
}

func (app *App) connexion(w http.ResponseWriter, r *http.Request) {
	u, err := app.auth.URLConnexion()
	if err != nil {
		pageMessage(w, "Connexion impossible", err.Error())
		return
	}
	http.Redirect(w, r, u, http.StatusFound)
}

func (app *App) retour(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		pageMessage(w, "Connexion refusée", e+" — "+q.Get("error_description"))
		return
	}
	if err := app.auth.Retour(r.Context(), q.Get("code"), q.Get("state")); err != nil {
		pageMessage(w, "Connexion impossible", err.Error())
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func pageMessage(w http.ResponseWriter, titre, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>Régie Partage</title>
<body style="font-family:system-ui;max-width:40rem;margin:4rem auto;padding:0 1rem">
<h1>%s</h1><p>%s</p><p><a href="/">Retour à Régie Partage</a></p>`, html.EscapeString(titre), html.EscapeString(msg))
}

// --- Données ---

func (app *App) donnees(r *http.Request) (any, error) {
	d, _, err := app.store.Charger(r.Context())
	if err != nil {
		return nil, err
	}
	TrierProjets(d.Projets)
	TrierRegisseurs(d.Regisseurs)
	return d, nil
}

func (app *App) enregistrerRegisseur(r *http.Request) (any, error) {
	var reg Regisseur
	if err := lireJSON(r, &reg); err != nil {
		return nil, err
	}
	if err := reg.Normaliser(); err != nil {
		return nil, err
	}
	creation := reg.ID == ""
	if creation {
		reg.ID = nouvelID("reg")
	}
	return app.store.Modifier(r.Context(), func(d *Donnees) error {
		for _, autre := range d.Regisseurs {
			if autre.ID != reg.ID && autre.Email == reg.Email {
				return fmt.Errorf("%s utilise déjà cette adresse e-mail", autre.NomComplet())
			}
		}
		if creation {
			d.Regisseurs = append(d.Regisseurs, reg)
			return nil
		}
		ex := d.Regisseur(reg.ID)
		if ex == nil {
			return ErrIntrouvable
		}
		if ex.Email != reg.Email && aDesAcces(d, reg.ID) {
			return errors.New("ce régisseur a des accès en cours : les retirer avant de changer son adresse e-mail")
		}
		*ex = reg
		return nil
	})
}

func aDesAcces(d *Donnees, regID string) bool {
	for _, p := range d.Projets {
		if p.AccesDe(regID) != nil {
			return true
		}
	}
	return false
}

func (app *App) supprimerRegisseur(r *http.Request) (any, error) {
	id := r.PathValue("id")
	d, _, err := app.store.Charger(r.Context())
	if err != nil {
		return nil, err
	}
	// Retire d'abord ses accès sur Microsoft 365.
	for i := range d.Projets {
		if err := app.retirer(r.Context(), d, &d.Projets[i], id); err != nil {
			return nil, fmt.Errorf("retrait de l'accès au projet « %s » impossible : %w", d.Projets[i].Nom, err)
		}
	}
	return app.store.Modifier(r.Context(), func(d *Donnees) error {
		for i := range d.Projets {
			d.Projets[i].Acces = sansAcces(d.Projets[i].Acces, id)
		}
		var garde []Regisseur
		for _, reg := range d.Regisseurs {
			if reg.ID != id {
				garde = append(garde, reg)
			}
		}
		d.Regisseurs = garde
		return nil
	})
}

// retirer supprime l'accès d'un régisseur chez le fournisseur.
func (app *App) retirer(ctx context.Context, d *Donnees, p *Projet, regID string) error {
	a := p.AccesDe(regID)
	if a == nil {
		return nil
	}
	return app.f().Retirer(ctx, p.DossierID, *a, accesCommun(p, *a), destinataire(d, regID))
}

// accesCommun : la permission est partagée avec un autre invité du projet.
func accesCommun(p *Projet, a Acces) bool {
	for _, autre := range p.Acces {
		if autre.RegisseurID != a.RegisseurID && autre.PermissionID == a.PermissionID {
			return true
		}
	}
	return false
}

func destinataire(d *Donnees, regID string) Destinataire {
	if r := d.Regisseur(regID); r != nil {
		return Destinataire{Email: r.Email, Nom: r.NomComplet()}
	}
	return Destinataire{}
}

func sansAcces(as []Acces, regID string) []Acces {
	var out []Acces
	for _, a := range as {
		if a.RegisseurID != regID {
			out = append(out, a)
		}
	}
	return out
}

func (app *App) enregistrerProjet(r *http.Request) (any, error) {
	var p Projet
	if err := lireJSON(r, &p); err != nil {
		return nil, err
	}
	if err := p.Normaliser(); err != nil {
		return nil, err
	}
	ctx := r.Context()

	if p.ID == "" {
		racine, err := app.f().AssurerRacine(ctx)
		if err != nil {
			return nil, err
		}
		dossier, err := app.f().CreerDossier(ctx, racine.ID, NomDossier(p))
		if err != nil {
			return nil, err
		}
		p.ID, p.DossierID, p.WebURL, p.Acces = nouvelID("prj"), dossier.ID, dossier.WebURL, nil
		p.CreeLe = time.Now().Format(time.RFC3339)
		d, err := app.store.Modifier(ctx, func(d *Donnees) error {
			d.Projets = append(d.Projets, p)
			return nil
		})
		if err != nil {
			_ = app.f().Supprimer(ctx, dossier.ID)
		}
		return d, err
	}

	actuel, _, err := app.store.Charger(ctx)
	if err != nil {
		return nil, err
	}
	ancien := actuel.Projet(p.ID)
	if ancien == nil {
		return nil, ErrIntrouvable
	}
	dossierID, webURL := ancien.DossierID, ancien.WebURL
	if NomDossier(*ancien) != NomDossier(p) {
		// Renommage au mieux : un conflit de nom laisse l'ancien dossier.
		if id, err := app.f().Renommer(ctx, ancien.DossierID, NomDossier(p)); err == nil && id != dossierID {
			dossierID = id
			if e, err := app.f().Element(ctx, id); err == nil {
				webURL = e.WebURL
			}
		}
	}
	return app.store.Modifier(ctx, func(d *Donnees) error {
		ex := d.Projet(p.ID)
		if ex == nil {
			return ErrIntrouvable
		}
		ex.Nom, ex.DateDebut, ex.DateFin, ex.Notes, ex.Archive = p.Nom, p.DateDebut, p.DateFin, p.Notes, p.Archive
		ex.DossierID, ex.WebURL = dossierID, webURL
		return nil
	})
}

// supprimerProjet retire tous les accès et oublie le projet ; le dossier et
// ses documents restent sur SharePoint (suppression manuelle si voulu).
func (app *App) supprimerProjet(r *http.Request) (any, error) {
	id := r.PathValue("id")
	d, _, err := app.store.Charger(r.Context())
	if err != nil {
		return nil, err
	}
	p := d.Projet(id)
	if p == nil {
		return nil, ErrIntrouvable
	}
	fait := map[string]bool{}
	for _, a := range p.Acces {
		if fait[a.PermissionID] {
			continue
		}
		fait[a.PermissionID] = true
		if err := app.f().Retirer(r.Context(), p.DossierID, a, false, destinataire(d, a.RegisseurID)); err != nil {
			return nil, err
		}
	}
	return app.store.Modifier(r.Context(), func(d *Donnees) error {
		var garde []Projet
		for _, x := range d.Projets {
			if x.ID != id {
				garde = append(garde, x)
			}
		}
		d.Projets = garde
		return nil
	})
}

func (app *App) projet(ctx context.Context, id string) (*Projet, error) {
	d, _, err := app.store.Charger(ctx)
	if err != nil {
		return nil, err
	}
	p := d.Projet(id)
	if p == nil {
		return nil, ErrIntrouvable
	}
	return p, nil
}

// --- Documents ---

func (app *App) documents(r *http.Request) (any, error) {
	p, err := app.projet(r.Context(), r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	els, err := app.f().Enfants(r.Context(), p.DossierID)
	if errors.Is(err, ErrIntrouvable) {
		return nil, errors.New("le dossier de ce projet a été supprimé ou déplacé sur SharePoint")
	}
	if els == nil {
		els = []Element{}
	}
	return els, err
}

// envoyerDocument reçoit le fichier brut (corps de la requête), nom en paramètre.
func (app *App) envoyerDocument(r *http.Request) (any, error) {
	p, err := app.projet(r.Context(), r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	nom := strings.TrimSpace(r.URL.Query().Get("nom"))
	if nom == "" || strings.ContainsAny(nom, `/\`) || r.ContentLength <= 0 {
		return nil, errors.New("fichier invalide (dossiers et fichiers vides non pris en charge)")
	}
	nom = strings.Map(func(c rune) rune {
		if strings.ContainsRune(`"*:<>?|#%`, c) || c < 32 {
			return '-'
		}
		return c
	}, nom)
	return app.f().Envoyer(r.Context(), p.DossierID, nom, r.ContentLength, r.Body)
}

func (app *App) supprimerDocument(r *http.Request) (any, error) {
	p, err := app.projet(r.Context(), r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	e, err := app.f().Element(r.Context(), r.PathValue("doc"))
	if err != nil {
		return nil, err
	}
	if e.Parent == nil || e.Parent.ID != p.DossierID {
		return nil, errors.New("ce document n'appartient pas au projet")
	}
	return nil, app.f().Supprimer(r.Context(), e.ID)
}

// --- Accès ---

func (app *App) messageDefaut(p *Projet, reg *Regisseur) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Bonjour %s,\n\nVoici l'accès au dossier de documents du projet « %s »", reg.Prenom, p.Nom)
	if p.DateDebut != "" {
		debut, _ := time.Parse("2006-01-02", p.DateDebut)
		if p.DateFin != "" && p.DateFin != p.DateDebut {
			fin, _ := time.Parse("2006-01-02", p.DateFin)
			fmt.Fprintf(&b, " (du %s au %s)", debut.Format("02/01/2006"), fin.Format("02/01/2006"))
		} else {
			fmt.Fprintf(&b, " (le %s)", debut.Format("02/01/2006"))
		}
	}
	if app.auth.Config().Nextcloud() {
		b.WriteString(".\nLe dossier est protégé par un mot de passe, communiqué séparément.")
	} else {
		b.WriteString(".\nÀ l'ouverture du lien, un code de vérification vous sera envoyé par e-mail : aucun compte à créer.")
	}
	if salle := app.auth.Config().NomSalle; salle != "" {
		b.WriteString("\n\n" + salle)
	}
	return b.String()
}

func (app *App) messageParDefaut(r *http.Request) (any, error) {
	d, _, err := app.store.Charger(r.Context())
	if err != nil {
		return nil, err
	}
	p, reg := d.Projet(r.PathValue("id")), d.Regisseur(r.PathValue("reg"))
	if p == nil || reg == nil {
		return nil, ErrIntrouvable
	}
	return map[string]string{"message": app.messageDefaut(p, reg)}, nil
}

type demandeAcces struct {
	RegisseurID string `json:"regisseurId"`
	Ecriture    bool   `json:"ecriture"`
	Message     string `json:"message"`
}

func (app *App) donnerAcces(r *http.Request) (any, error) {
	var dem demandeAcces
	if err := lireJSON(r, &dem); err != nil {
		return nil, err
	}
	if len([]rune(dem.Message)) > 2000 {
		return nil, errors.New("message trop long (2000 caractères maximum)")
	}
	ctx := r.Context()
	d, _, err := app.store.Charger(ctx)
	if err != nil {
		return nil, err
	}
	p, reg := d.Projet(r.PathValue("id")), d.Regisseur(dem.RegisseurID)
	if p == nil || reg == nil {
		return nil, ErrIntrouvable
	}
	if strings.TrimSpace(dem.Message) == "" {
		dem.Message = app.messageDefaut(p, reg)
	}
	dest := destinataire(d, reg.ID)
	var partage *Partage
	if ancien := p.AccesDe(reg.ID); ancien != nil {
		partage, err = app.f().ChangerDroit(ctx, p.DossierID, *ancien, accesCommun(p, *ancien), dest, dem.Ecriture, dem.Message)
	} else {
		partage, err = app.f().Partager(ctx, p.DossierID, dest, dem.Ecriture, dem.Message)
	}
	if err != nil {
		if p.AccesDe(reg.ID) != nil && !app.auth.Config().Nextcloud() {
			// Microsoft : l'ancien accès a pu être retiré avant l'échec.
			_, _ = app.store.Modifier(ctx, func(d *Donnees) error {
				if x := d.Projet(p.ID); x != nil {
					x.Acces = sansAcces(x.Acces, reg.ID)
				}
				return nil
			})
		}
		return nil, err
	}
	return app.store.Modifier(ctx, func(d *Donnees) error {
		x := d.Projet(p.ID)
		if x == nil {
			return ErrIntrouvable
		}
		x.Acces = append(sansAcces(x.Acces, reg.ID), partage.versAcces(reg.ID, dem.Ecriture, time.Now().Format(time.RFC3339)))
		return nil
	})
}

func (app *App) renvoyerAcces(r *http.Request) (any, error) {
	ctx := r.Context()
	d, _, err := app.store.Charger(ctx)
	if err != nil {
		return nil, err
	}
	p, reg := d.Projet(r.PathValue("id")), d.Regisseur(r.PathValue("reg"))
	if p == nil || reg == nil || p.AccesDe(reg.ID) == nil {
		return nil, ErrIntrouvable
	}
	a := p.AccesDe(reg.ID)
	partage, err := app.f().Renvoyer(ctx, p.DossierID, *a, destinataire(d, reg.ID), app.messageDefaut(p, reg))
	if err != nil {
		return nil, err
	}
	return app.store.Modifier(ctx, func(d *Donnees) error {
		if x := d.Projet(p.ID); x != nil {
			if a := x.AccesDe(reg.ID); a != nil {
				*a = partage.versAcces(reg.ID, a.Ecriture, time.Now().Format(time.RFC3339))
			}
		}
		return nil
	})
}

func (app *App) retirerAcces(r *http.Request) (any, error) {
	ctx := r.Context()
	d, _, err := app.store.Charger(ctx)
	if err != nil {
		return nil, err
	}
	p := d.Projet(r.PathValue("id"))
	if p == nil {
		return nil, ErrIntrouvable
	}
	regID := r.PathValue("reg")
	if err := app.retirer(ctx, d, p, regID); err != nil {
		return nil, err
	}
	return app.store.Modifier(ctx, func(d *Donnees) error {
		if x := d.Projet(p.ID); x != nil {
			x.Acces = sansAcces(x.Acces, regID)
		}
		return nil
	})
}
