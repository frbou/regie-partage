package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// ncFactice : faux serveur Nextcloud en mémoire (WebDAV + OCS + Login Flow v2),
// assez fidèle pour les tests et la démonstration.
type ncFactice struct {
	mu        sync.Mutex
	srv       *httptest.Server
	fichiers  map[string][]byte // chemin → contenu ; dossiers : nil
	versions  map[string]int
	partages  map[string]url.Values
	n         int
	sansMail  bool // application « Share by mail » désactivée
	sondages  int  // nombre de sondages avant que la connexion aboutisse
	mailsEnv  []string
	morceaux  map[string]map[string][]byte
	mdpPublic string
}

func nouveauNCFactice() *ncFactice {
	f := &ncFactice{fichiers: map[string][]byte{"": nil}, versions: map[string]int{}, partages: map[string]url.Values{},
		morceaux: map[string]map[string][]byte{}, sondages: 1}
	f.srv = httptest.NewServer(f)
	return f
}

func (f *ncFactice) estDossier(p string) bool {
	c, ok := f.fichiers[p]
	return ok && c == nil
}

func (f *ncFactice) etag(p string) string { return fmt.Sprintf(`"%x-%d"`, len(p), f.versions[p]) }

func ocsRepondre(w http.ResponseWriter, statut int, msg string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statut)
	_ = json.NewEncoder(w).Encode(map[string]any{"ocs": map[string]any{
		"meta": map[string]any{"statuscode": statut, "message": msg}, "data": data}})
}

func (f *ncFactice) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path

	switch {
	case p == "/index.php/login/v2":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"poll":  map[string]string{"token": "jt", "endpoint": f.srv.URL + "/index.php/login/v2/poll"},
			"login": f.srv.URL + "/index.php/login/v2/flow/abc"})
		return
	case p == "/index.php/login/v2/poll":
		if f.sondages > 0 {
			f.sondages--
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"server": f.srv.URL, "loginName": "fred", "appPassword": "mdp-app"})
		return
	}

	if u, m, ok := r.BasicAuth(); !ok || u != "fred" || m != "mdp-app" {
		w.WriteHeader(401)
		return
	}

	const dav = "/remote.php/dav/files/fred"
	const up = "/remote.php/dav/uploads/fred/"
	switch {
	case p == "/ocs/v2.php/cloud/user":
		ocsRepondre(w, 200, "OK", map[string]string{"id": "fred", "displayname": "Fred B", "email": "fred@exemple.fr"})
	case p == "/ocs/v2.php/core/apppassword":
		ocsRepondre(w, 200, "OK", []any{})
	case strings.HasPrefix(p, "/ocs/v2.php/apps/files_sharing/api/v1/shares"):
		_ = r.ParseForm()
		id := strings.TrimPrefix(strings.TrimPrefix(p, "/ocs/v2.php/apps/files_sharing/api/v1/shares"), "/")
		switch {
		case r.Method == "POST" && id == "":
			if r.PostForm.Get("shareType") == "4" && f.sansMail {
				ocsRepondre(w, 403, "Sharing by mail is not enabled", nil)
				return
			}
			if !f.estDossier(strings.TrimPrefix(r.PostForm.Get("path"), "/")) {
				ocsRepondre(w, 404, "Wrong path, file/folder does not exist", nil)
				return
			}
			f.n++
			nid := fmt.Sprint(f.n)
			f.partages[nid] = r.PostForm
			if r.PostForm.Get("shareType") == "4" {
				f.mailsEnv = append(f.mailsEnv, r.PostForm.Get("shareWith"))
			}
			ocsRepondre(w, 200, "OK", map[string]any{"id": f.n, "token": "tok" + nid, "url": ""})
		case strings.HasSuffix(id, "/send-email"):
			pid := strings.TrimSuffix(id, "/send-email")
			if _, ok := f.partages[pid]; !ok {
				ocsRepondre(w, 404, "Wrong share ID", nil)
				return
			}
			f.mailsEnv = append(f.mailsEnv, f.partages[pid].Get("shareWith"))
			ocsRepondre(w, 200, "OK", []any{})
		case r.Method == "PUT":
			if _, ok := f.partages[id]; !ok {
				ocsRepondre(w, 404, "Wrong share ID", nil)
				return
			}
			f.partages[id].Set("permissions", r.PostForm.Get("permissions"))
			ocsRepondre(w, 200, "OK", map[string]any{"id": id})
		case r.Method == "DELETE":
			if _, ok := f.partages[id]; !ok {
				ocsRepondre(w, 404, "Wrong share ID", nil)
				return
			}
			delete(f.partages, id)
			ocsRepondre(w, 200, "OK", []any{})
		}

	case strings.HasPrefix(p, up):
		rel := strings.TrimPrefix(p, up)
		dossier, morceau, _ := strings.Cut(rel, "/")
		switch r.Method {
		case "MKCOL":
			f.morceaux[dossier] = map[string][]byte{}
			w.WriteHeader(201)
		case "PUT":
			b, _ := io.ReadAll(r.Body)
			f.morceaux[dossier][morceau] = b
			w.WriteHeader(201)
		case "DELETE":
			delete(f.morceaux, dossier)
			w.WriteHeader(204)
		case "MOVE":
			dest := f.cheminDestination(r)
			var noms []string
			for k := range f.morceaux[dossier] {
				noms = append(noms, k)
			}
			sort.Strings(noms)
			var tout []byte
			for _, k := range noms {
				tout = append(tout, f.morceaux[dossier][k]...)
			}
			if r.Header.Get("OC-Total-Length") != fmt.Sprint(len(tout)) {
				w.WriteHeader(400)
				return
			}
			f.fichiers[dest] = tout
			f.versions[dest]++
			delete(f.morceaux, dossier)
			w.WriteHeader(201)
		}

	case strings.HasPrefix(p, dav):
		rel := strings.Trim(strings.TrimPrefix(p, dav), "/")
		switch r.Method {
		case "PROPFIND":
			if _, ok := f.fichiers[rel]; !ok {
				w.WriteHeader(404)
				return
			}
			chemins := []string{rel}
			if r.Header.Get("Depth") == "1" && f.estDossier(rel) {
				for k := range f.fichiers {
					if k != "" && k != rel && path.Dir(k) == rel || (rel == "" && k != "" && !strings.Contains(k, "/")) {
						chemins = append(chemins, k)
					}
				}
			}
			var b strings.Builder
			b.WriteString(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">`)
			for _, c := range chemins {
				href := dav + "/" + cheminEncode(c)
				typ, taille := "", fmt.Sprintf("<d:getcontentlength>%d</d:getcontentlength>", len(f.fichiers[c]))
				if f.estDossier(c) {
					href += "/"
					typ, taille = "<d:collection/>", "<oc:size>0</oc:size>"
				}
				fmt.Fprintf(&b, `<d:response><d:href>%s</d:href><d:propstat><d:prop><d:getlastmodified>%s</d:getlastmodified>%s<d:resourcetype>%s</d:resourcetype><d:getetag>%s</d:getetag><oc:fileid>%d</oc:fileid></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`,
					href, time.Now().UTC().Format(http.TimeFormat), taille, typ, f.etag(c), len(c))
			}
			b.WriteString(`</d:multistatus>`)
			w.WriteHeader(207)
			_, _ = io.WriteString(w, b.String())
		case "MKCOL":
			if _, ok := f.fichiers[rel]; ok {
				w.WriteHeader(405)
				return
			}
			if !f.estDossier(path.Dir(rel)) && strings.Contains(rel, "/") {
				w.WriteHeader(409)
				return
			}
			f.fichiers[rel] = nil
			w.WriteHeader(201)
		case "GET":
			c, ok := f.fichiers[rel]
			if !ok || c == nil {
				w.WriteHeader(404)
				return
			}
			w.Header().Set("ETag", f.etag(rel))
			_, _ = w.Write(c)
		case "PUT":
			_, existe := f.fichiers[rel]
			if (r.Header.Get("If-None-Match") == "*" && existe) ||
				(r.Header.Get("If-Match") != "" && (!existe || r.Header.Get("If-Match") != f.etag(rel))) {
				w.WriteHeader(412)
				return
			}
			b, _ := io.ReadAll(r.Body)
			if b == nil {
				b = []byte{}
			}
			f.fichiers[rel] = b
			f.versions[rel]++
			w.WriteHeader(201)
		case "DELETE":
			if _, ok := f.fichiers[rel]; !ok {
				w.WriteHeader(404)
				return
			}
			for k := range f.fichiers {
				if k == rel || strings.HasPrefix(k, rel+"/") {
					delete(f.fichiers, k)
				}
			}
			w.WriteHeader(204)
		case "MOVE":
			dest := f.cheminDestination(r)
			if _, ok := f.fichiers[dest]; ok {
				w.WriteHeader(412)
				return
			}
			for k, v := range f.fichiers {
				if k == rel || strings.HasPrefix(k, rel+"/") {
					f.fichiers[dest+strings.TrimPrefix(k, rel)] = v
					delete(f.fichiers, k)
				}
			}
			w.WriteHeader(201)
		}
	default:
		w.WriteHeader(404)
	}
}

func (f *ncFactice) cheminDestination(r *http.Request) string {
	u, _ := url.Parse(r.Header.Get("Destination"))
	return strings.Trim(strings.TrimPrefix(u.Path, "/remote.php/dav/files/fred"), "/")
}

// ncConnecte : client Nextcloud déjà connecté au faux serveur.
func ncConnecte(t *testing.T) (*ncFactice, *Nextcloud) {
	t.Helper()
	f := nouveauNCFactice()
	t.Cleanup(f.srv.Close)
	a := &Auth{dir: t.TempDir(), cfg: Config{Fournisseur: fournNextcloud, NextcloudURL: f.srv.URL, DossierRacine: "Régie Partage"},
		nc: IdentNextcloud{Login: "fred", MotDePasse: "mdp-app", UserID: "fred"}}
	return f, NouveauNextcloud(a)
}

func TestNextcloudConnexion(t *testing.T) {
	f := nouveauNCFactice()
	defer f.srv.Close()
	a := &Auth{dir: t.TempDir(), cfg: Config{Fournisseur: fournNextcloud, NextcloudURL: f.srv.URL, DossierRacine: "R"}}
	n := NouveauNextcloud(a)
	u, err := n.DemarrerConnexion(context.Background())
	if err != nil || !strings.Contains(u, "/login/v2/flow/") {
		t.Fatalf("DemarrerConnexion = %q, %v", u, err)
	}
	limite := time.Now().Add(15 * time.Second)
	for n.EtatConnexion() == "attente" && time.Now().Before(limite) {
		time.Sleep(100 * time.Millisecond)
	}
	if e := n.EtatConnexion(); e != "ok" {
		t.Fatalf("état de connexion : %q", e)
	}
	c := a.Compte()
	id, _ := a.Nextcloud()
	if c == nil || c.Nom != "Fred B" || id.UserID != "fred" || id.MotDePasse != "mdp-app" {
		t.Fatalf("compte %+v, identité %+v", c, id)
	}
	// La connexion est enregistrée et relue au démarrage suivant.
	a2, err := NouvelleAuth(a.dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if id2, err := a2.Nextcloud(); err != nil || id2.MotDePasse != "mdp-app" {
		t.Fatalf("identité relue : %+v, %v", id2, err)
	}
}

func TestNextcloudDossiersEtDocuments(t *testing.T) {
	f, n := ncConnecte(t)
	ctx := context.Background()
	racine, err := n.AssurerRacine(ctx)
	if err != nil || racine.ID != "Régie Partage" || racine.Dossier == nil {
		t.Fatalf("racine %+v, %v", racine, err)
	}
	d1, err := n.CreerDossier(ctx, racine.ID, "2026-11 La demande")
	if err != nil {
		t.Fatal(err)
	}
	d2, err := n.CreerDossier(ctx, racine.ID, "2026-11 La demande")
	if err != nil || d2.ID != "Régie Partage/2026-11 La demande 2" {
		t.Fatalf("doublon : %+v, %v", d2, err)
	}

	petit := []byte("plan de feu")
	e, err := n.Envoyer(ctx, d1.ID, "Plan feu.pdf", int64(len(petit)), bytes.NewReader(petit))
	if err != nil || e.Taille != int64(len(petit)) || e.Parent == nil || e.Parent.ID != d1.ID {
		t.Fatalf("envoi simple : %+v, %v", e, err)
	}
	e2, err := n.Envoyer(ctx, d1.ID, "Plan feu.pdf", int64(len(petit)), bytes.NewReader(petit))
	if err != nil || e2.Nom != "Plan feu 2.pdf" {
		t.Fatalf("envoi doublon : %+v, %v", e2, err)
	}
	gros := bytes.Repeat([]byte("abcdefghij"), 2_500_000+123) // ~25 Mo → 3 morceaux
	e3, err := n.Envoyer(ctx, d1.ID, "Vidéo.mov", int64(len(gros)), bytes.NewReader(gros))
	if err != nil || !bytes.Equal(f.fichiers[e3.ID], gros) {
		t.Fatalf("envoi par morceaux : %v (reçu %d octets)", err, len(f.fichiers["Régie Partage/2026-11 La demande/Vidéo.mov"]))
	}
	if len(f.morceaux) != 0 {
		t.Error("dossier temporaire d'envoi non nettoyé")
	}

	enfants, err := n.Enfants(ctx, d1.ID)
	if err != nil || len(enfants) != 3 {
		t.Fatalf("enfants : %+v, %v", enfants, err)
	}
	for _, x := range enfants {
		if x.WebURL == "" || x.Parent == nil || x.Parent.ID != d1.ID {
			t.Errorf("élément incomplet : %+v", x)
		}
	}

	nouveau, err := n.Renommer(ctx, d1.ID, "2026-12 La demande")
	if err != nil || nouveau != "Régie Partage/2026-12 La demande" {
		t.Fatalf("renommage : %q, %v", nouveau, err)
	}
	if _, ok := f.fichiers[nouveau+"/Plan feu.pdf"]; !ok {
		t.Error("les documents doivent suivre le dossier renommé")
	}
	if err := n.Supprimer(ctx, nouveau+"/Plan feu.pdf"); err != nil {
		t.Fatal(err)
	}
	if err := n.Supprimer(ctx, nouveau+"/Plan feu.pdf"); err != nil {
		t.Errorf("supprimer un absent ne doit pas échouer : %v", err)
	}
}

func TestNextcloudDonneesConcurrentes(t *testing.T) {
	f, n := ncConnecte(t)
	s := &Store{stock: stockageFournisseur{f: func() Fournisseur { return n }}}
	ctx := context.Background()
	if _, err := s.Modifier(ctx, func(d *Donnees) error {
		d.Regisseurs = append(d.Regisseurs, Regisseur{ID: "a"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Un autre poste écrit entre la lecture et l'écriture.
	premier := true
	d, err := s.Modifier(ctx, func(d *Donnees) error {
		if premier {
			premier = false
			f.mu.Lock()
			var autre Donnees
			_ = json.Unmarshal(f.fichiers["Régie Partage/"+fichierDonnees], &autre)
			autre.Regisseurs = append(autre.Regisseurs, Regisseur{ID: "b"})
			f.fichiers["Régie Partage/"+fichierDonnees], _ = json.Marshal(autre)
			f.versions["Régie Partage/"+fichierDonnees]++
			f.mu.Unlock()
		}
		d.Regisseurs = append(d.Regisseurs, Regisseur{ID: "c"})
		return nil
	})
	if err != nil || len(d.Regisseurs) != 3 {
		t.Fatalf("après conflit : %+v, %v", d, err)
	}
}

func TestNextcloudPartages(t *testing.T) {
	f, n := ncConnecte(t)
	ctx := context.Background()
	racine, _ := n.AssurerRacine(ctx)
	dos, _ := n.CreerDossier(ctx, racine.ID, "Projet")
	lea := Destinataire{Email: "lea@exemple.fr", Nom: "Léa Martin"}

	p, err := n.Partager(ctx, dos.ID, lea, false, "Bonjour Léa")
	if err != nil || p.SansMail || p.MotDePasse == "" || !strings.HasSuffix(p.Lien, "/index.php/s/tok1") {
		t.Fatalf("partage par e-mail : %+v, %v", p, err)
	}
	pf := f.partages[p.ID]
	if pf.Get("shareType") != "4" || pf.Get("shareWith") != lea.Email || pf.Get("permissions") != "1" ||
		pf.Get("password") != p.MotDePasse || pf.Get("note") != "Bonjour Léa" || pf.Get("path") != "/Projet" && pf.Get("path") != "/"+dos.ID {
		t.Errorf("paramètres du partage : %v", pf)
	}

	a := p.versAcces("lea", false, "")
	if _, err := n.ChangerDroit(ctx, dos.ID, a, false, lea, true, ""); err != nil || f.partages[p.ID].Get("permissions") != "15" {
		t.Fatalf("changement de droit : %v %v", err, f.partages[p.ID])
	}
	if r, err := n.Renvoyer(ctx, dos.ID, a, lea, ""); err != nil || r.SansMail || len(f.mailsEnv) != 2 {
		t.Fatalf("renvoi : %+v, %v, mails %v", r, err, f.mailsEnv)
	}
	if err := n.Retirer(ctx, dos.ID, a, false, lea); err != nil || len(f.partages) != 0 {
		t.Fatalf("retrait : %v", err)
	}
	if err := n.Retirer(ctx, dos.ID, a, false, lea); err != nil {
		t.Errorf("retirer un partage déjà supprimé ne doit pas échouer : %v", err)
	}

	// Sans « Share by mail » : lien personnel nommé, à envoyer soi-même.
	f.sansMail = true
	p, err = n.Partager(ctx, dos.ID, lea, true, "")
	if err != nil || !p.SansMail || p.Lien == "" {
		t.Fatalf("repli lien : %+v, %v", p, err)
	}
	pf = f.partages[p.ID]
	if pf.Get("shareType") != "3" || pf.Get("label") != "Léa Martin" || pf.Get("permissions") != "15" {
		t.Errorf("paramètres du lien : %v", pf)
	}
	if r, _ := n.Renvoyer(ctx, dos.ID, p.versAcces("lea", true, ""), lea, ""); !r.SansMail {
		t.Error("un lien personnel se renvoie soi-même")
	}
}

func TestMotDePasseLisible(t *testing.T) {
	vus := map[string]bool{}
	for i := 0; i < 200; i++ {
		m := motDePasseLisible()
		if len(m) != 14 || strings.ContainsAny(m, "0O1lI") || vus[m] {
			t.Fatalf("mot de passe %q", m)
		}
		vus[m] = true
	}
}
