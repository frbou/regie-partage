package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// grapheFactice démarre un faux Microsoft Graph et renvoie un client branché dessus.
func grapheFactice(t *testing.T, h http.HandlerFunc) *Graphe {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	ancien := grapheBase
	grapheBase = srv.URL
	t.Cleanup(func() { grapheBase = ancien })
	a := &Auth{acces: "jeton", expire: time.Now().Add(time.Hour), refresh: "r",
		cfg: Config{TenantID: "t", ClientID: "c", DossierRacine: "Régie Partage", SiteURL: "https://salle.sharepoint.com/sites/Regie"}}
	return NouveauGraphe(a)
}

func TestLecteurSiteSharePoint(t *testing.T) {
	g := grapheFactice(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sites/salle.sharepoint.com:/sites/Regie":
			fmt.Fprint(w, `{"id":"site1"}`)
		case "/sites/site1/drive":
			fmt.Fprint(w, `{"id":"drv"}`)
		default:
			t.Errorf("appel inattendu %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	id, err := g.Lecteur(context.Background())
	if err != nil || id != "drv" {
		t.Fatalf("Lecteur = %q, %v", id, err)
	}
}

func TestEnvoiParMorceaux(t *testing.T) {
	taille := 2*tailleMorceau + 12345
	contenu := bytes.Repeat([]byte("x"), taille)
	var recu bytes.Buffer
	var plages []string
	var srvURL string
	g := grapheFactice(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/drive"):
			fmt.Fprint(w, `{"id":"drv"}`)
		case strings.HasPrefix(r.URL.Path, "/sites/"):
			fmt.Fprint(w, `{"id":"site1"}`)
		case strings.HasSuffix(r.URL.Path, "/createUploadSession"):
			if !strings.Contains(r.URL.Path, "/items/dossier1:/Plan feu.pdf:") {
				t.Errorf("chemin de session : %s", r.URL.Path)
			}
			fmt.Fprintf(w, `{"uploadUrl":%q}`, srvURL+"/session")
		case r.URL.Path == "/session":
			if r.Header.Get("Authorization") != "" {
				t.Error("la session de téléversement ne doit pas recevoir le jeton")
			}
			plages = append(plages, r.Header.Get("Content-Range"))
			n, _ := io.Copy(&recu, r.Body)
			if recu.Len() < taille {
				if n%(320<<10) != 0 {
					t.Errorf("morceau de %d octets, pas multiple de 320 Kio", n)
				}
				w.WriteHeader(202)
				return
			}
			w.WriteHeader(201)
			fmt.Fprint(w, `{"id":"f1","name":"Plan feu.pdf"}`)
		default:
			t.Errorf("appel inattendu %s %s", r.Method, r.URL.Path)
		}
	})
	srvURL = grapheBase
	e, err := g.Envoyer(context.Background(), "dossier1", "Plan feu.pdf", int64(taille), bytes.NewReader(contenu))
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != "f1" || recu.Len() != taille || len(plages) != 3 {
		t.Fatalf("envoi : id=%s reçu=%d plages=%v", e.ID, recu.Len(), plages)
	}
	if want := fmt.Sprintf("bytes %d-%d/%d", 2*tailleMorceau, taille-1, taille); plages[2] != want {
		t.Errorf("dernière plage %q, attendu %q", plages[2], want)
	}
}

func TestInviterEtRetirer(t *testing.T) {
	var corps map[string]any
	supprime := ""
	g := grapheFactice(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/drive"):
			fmt.Fprint(w, `{"id":"drv"}`)
		case strings.HasPrefix(r.URL.Path, "/sites/"):
			fmt.Fprint(w, `{"id":"site1"}`)
		case r.URL.Path == "/drives/drv/items/d1/invite":
			_ = json.NewDecoder(r.Body).Decode(&corps)
			fmt.Fprint(w, `{"value":[{"id":"perm9"}]}`)
		case r.Method == "DELETE" && r.URL.Path == "/drives/drv/items/d1/permissions/perm9":
			supprime = "perm9"
			w.WriteHeader(204)
		case r.Method == "DELETE":
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"code":"itemNotFound","message":"x"}}`)
		}
	})
	ctx := context.Background()
	perm, err := g.Inviter(ctx, "d1", "lea@exemple.fr", true, true, "Bonjour")
	if err != nil || perm != "perm9" {
		t.Fatalf("Inviter = %q, %v", perm, err)
	}
	if corps["requireSignIn"] != true || corps["sendInvitation"] != true ||
		corps["roles"].([]any)[0] != "write" || corps["message"] != "Bonjour" {
		t.Errorf("corps d'invitation : %v", corps)
	}
	if err := g.RetirerPermission(ctx, "d1", "perm9"); err != nil || supprime != "perm9" {
		t.Fatalf("retrait : %v", err)
	}
	if err := g.RetirerPermission(ctx, "d1", "disparue"); err != nil {
		t.Errorf("un accès déjà retiré ne doit pas être une erreur : %v", err)
	}
}
