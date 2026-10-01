package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"time"
)

// Données métier, stockées dans un unique fichier JSON du dossier racine
// SharePoint. Chaque modification relit le fichier, applique le changement
// et réécrit seulement si personne ne l'a modifié entre-temps (eTag) ;
// sinon elle recommence. Deux postes peuvent donc travailler en même temps.

const fichierDonnees = "_regie-partage.json"

type Regisseur struct {
	ID        string `json:"id"`
	Prenom    string `json:"prenom"`
	Nom       string `json:"nom"`
	Email     string `json:"email"`
	Metier    string `json:"metier"`
	Telephone string `json:"telephone"`
	Notes     string `json:"notes"`
}

type Acces struct {
	RegisseurID  string `json:"regisseurId"`
	Ecriture     bool   `json:"ecriture"` // false = lecture seule
	PermissionID string `json:"permissionId"`
	EnvoyeLe     string `json:"envoyeLe"`
}

type Projet struct {
	ID        string  `json:"id"`
	Nom       string  `json:"nom"`
	DateDebut string  `json:"dateDebut"`
	DateFin   string  `json:"dateFin"`
	Notes     string  `json:"notes"`
	Archive   bool    `json:"archive"`
	DossierID string  `json:"dossierId"`
	WebURL    string  `json:"webUrl"`
	Acces     []Acces `json:"acces"`
	CreeLe    string  `json:"creeLe"`
}

type Donnees struct {
	Version    int         `json:"version"`
	Regisseurs []Regisseur `json:"regisseurs"`
	Projets    []Projet    `json:"projets"`
	MajLe      string      `json:"majLe"`
	MajPar     string      `json:"majPar"`
}

func (d *Donnees) Regisseur(id string) *Regisseur {
	for i := range d.Regisseurs {
		if d.Regisseurs[i].ID == id {
			return &d.Regisseurs[i]
		}
	}
	return nil
}

func (d *Donnees) Projet(id string) *Projet {
	for i := range d.Projets {
		if d.Projets[i].ID == id {
			return &d.Projets[i]
		}
	}
	return nil
}

func (p *Projet) AccesDe(regID string) *Acces {
	for i := range p.Acces {
		if p.Acces[i].RegisseurID == regID {
			return &p.Acces[i]
		}
	}
	return nil
}

// Stockage : fichier distant avec contrôle de version (eTag).
type Stockage interface {
	Lire(ctx context.Context) (contenu []byte, etag string, err error) // ErrIntrouvable si absent
	Ecrire(ctx context.Context, contenu []byte, etag string) error     // ErrConflit si changé
}

type Store struct {
	stock Stockage
	qui   func() string
}

func (s *Store) Charger(ctx context.Context) (*Donnees, string, error) {
	b, etag, err := s.stock.Lire(ctx)
	if errors.Is(err, ErrIntrouvable) {
		d := &Donnees{Version: 1}
		d.completer()
		return d, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var d Donnees
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, "", fmt.Errorf("fichier %s illisible : %w", fichierDonnees, err)
	}
	d.completer()
	return &d, etag, nil
}

// completer remplace les listes absentes par des listes vides (pour l'interface).
func (d *Donnees) completer() {
	if d.Regisseurs == nil {
		d.Regisseurs = []Regisseur{}
	}
	if d.Projets == nil {
		d.Projets = []Projet{}
	}
	for i := range d.Projets {
		if d.Projets[i].Acces == nil {
			d.Projets[i].Acces = []Acces{}
		}
	}
}

// Modifier applique f sur des données fraîches et enregistre, en recommençant
// si un autre poste a écrit entre-temps. f doit être rejouable sans effet de
// bord externe (les appels Microsoft 365 se font avant ou après).
func (s *Store) Modifier(ctx context.Context, f func(*Donnees) error) (*Donnees, error) {
	for essai := 0; essai < 5; essai++ {
		d, etag, err := s.Charger(ctx)
		if err != nil {
			return nil, err
		}
		if err := f(d); err != nil {
			return nil, err
		}
		d.completer()
		d.MajLe = time.Now().Format(time.RFC3339)
		if s.qui != nil {
			d.MajPar = s.qui()
		}
		b, _ := json.MarshalIndent(d, "", "  ")
		err = s.stock.Ecrire(ctx, b, etag)
		if errors.Is(err, ErrConflit) {
			time.Sleep(time.Duration(200*(essai+1)) * time.Millisecond)
			continue
		}
		if err != nil {
			return nil, err
		}
		return d, nil
	}
	return nil, errors.New("enregistrement impossible : trop de modifications simultanées, réessayer")
}

// --- Validation ---

func (r *Regisseur) Normaliser() error {
	r.Prenom = strings.TrimSpace(r.Prenom)
	r.Nom = strings.TrimSpace(r.Nom)
	r.Email = strings.ToLower(strings.TrimSpace(r.Email))
	r.Metier = strings.TrimSpace(r.Metier)
	r.Telephone = strings.TrimSpace(r.Telephone)
	if r.Prenom == "" && r.Nom == "" {
		return errors.New("nom ou prénom obligatoire")
	}
	if a, err := mail.ParseAddress(r.Email); err != nil || a.Address != r.Email {
		return errors.New("adresse e-mail invalide")
	}
	return nil
}

func (r Regisseur) NomComplet() string {
	return strings.TrimSpace(r.Prenom + " " + r.Nom)
}

func (p *Projet) Normaliser() error {
	p.Nom = strings.TrimSpace(p.Nom)
	if p.Nom == "" {
		return errors.New("nom du projet obligatoire")
	}
	for _, d := range []string{p.DateDebut, p.DateFin} {
		if d != "" {
			if _, err := time.Parse("2006-01-02", d); err != nil {
				return errors.New("date invalide")
			}
		}
	}
	if p.DateDebut != "" && p.DateFin != "" && p.DateFin < p.DateDebut {
		return errors.New("la date de fin précède la date de début")
	}
	return nil
}

// NomDossier : nom du dossier SharePoint d'un projet, sans caractères interdits.
func NomDossier(p Projet) string {
	nom := p.Nom
	if p.DateDebut != "" {
		nom = p.DateDebut[:7] + " " + nom
	}
	nom = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`"*:<>?/\|#%`, r) || r < 32 {
			return '-'
		}
		return r
	}, nom)
	nom = strings.Trim(strings.TrimSpace(nom), ".")
	if len([]rune(nom)) > 120 {
		nom = string([]rune(nom)[:120])
	}
	if nom == "" {
		nom = "Projet"
	}
	return nom
}

func TrierProjets(ps []Projet) {
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].Archive != ps[j].Archive {
			return !ps[i].Archive
		}
		a, b := ps[i].DateDebut, ps[j].DateDebut
		if a != b {
			if a == "" {
				return false
			}
			if b == "" {
				return true
			}
			return a < b
		}
		return strings.ToLower(ps[i].Nom) < strings.ToLower(ps[j].Nom)
	})
}

func TrierRegisseurs(rs []Regisseur) {
	sort.SliceStable(rs, func(i, j int) bool {
		a := strings.ToLower(rs[i].Nom + " " + rs[i].Prenom)
		b := strings.ToLower(rs[j].Nom + " " + rs[j].Prenom)
		return a < b
	})
}

func nouvelID(prefixe string) string {
	return prefixe + "_" + strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(aleatoire(9)))
}

// --- Stockage SharePoint ---

type stockageGraphe struct {
	g    *Graphe
	auth *Auth
}

func (s stockageGraphe) chemin() string {
	return s.auth.Config().DossierRacine + "/" + fichierDonnees
}

func (s stockageGraphe) Lire(ctx context.Context) ([]byte, string, error) {
	return s.g.LireFichier(ctx, s.chemin())
}

func (s stockageGraphe) Ecrire(ctx context.Context, b []byte, etag string) error {
	if _, err := s.g.AssurerDossier(ctx, s.auth.Config().DossierRacine); err != nil {
		return err
	}
	_, err := s.g.EcrireFichier(ctx, s.chemin(), b, etag)
	return err
}
