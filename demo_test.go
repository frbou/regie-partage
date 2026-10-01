package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestDemo lance l'application complète contre un faux SharePoint en mémoire,
// pour essayer l'interface sans compte Microsoft 365 :
//
//	DEMO=1 go test -run TestDemo -timeout 0
//
// puis ouvrir http://localhost:47820/.
func TestDemo(t *testing.T) {
	if os.Getenv("DEMO") == "" {
		t.Skip("DEMO=1 pour lancer la démonstration")
	}
	faux := httptest.NewServer(nouveauSharePointFactice())
	defer faux.Close()
	grapheBase = faux.URL

	dir := t.TempDir()
	app, err := NouvelleApp(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = app.auth.EnregistrerConfig(Config{TenantID: "demo", ClientID: "demo", SiteURL: "https://salle.sharepoint.com/sites/Regie", NomSalle: "La régie du Théâtre"})
	app.auth.refresh, app.auth.acces, app.auth.expire = "demo", "demo", time.Now().Add(24*time.Hour)
	app.auth.compte = &Compte{Nom: "Régie démo", Email: "regie@salle.fr"}

	mux := http.NewServeMux()
	app.Routes(mux)
	mux.Handle("/", http.FileServer(http.Dir("web")))
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: verifierHote(mux)}
	app.quitter = func() { go srv.Close() }
	fmt.Printf("Démo : http://localhost:%d/\n", port)
	_ = srv.Serve(ln)
}

type elementFactice struct {
	ID, Nom, Parent string
	Dossier         bool
	Contenu         []byte
	Version         int
	Modif           time.Time
	Perms           map[string]string
}

type sharePointFactice struct {
	mu   sync.Mutex
	elts map[string]*elementFactice
	n    int
}

func nouveauSharePointFactice() *sharePointFactice {
	return &sharePointFactice{elts: map[string]*elementFactice{"root": {ID: "root", Dossier: true}}}
}

func (s *sharePointFactice) id() string { s.n++; return fmt.Sprintf("it%d", s.n) }

func (s *sharePointFactice) json(e *elementFactice) map[string]any {
	m := map[string]any{"id": e.ID, "name": e.Nom, "size": len(e.Contenu), "eTag": fmt.Sprintf("\"%s,%d\"", e.ID, e.Version),
		"lastModifiedDateTime": e.Modif.Format(time.RFC3339), "webUrl": "https://salle.sharepoint.com/f/" + e.ID,
		"parentReference": map[string]string{"id": e.Parent}, "lastModifiedBy": map[string]any{"user": map[string]string{"displayName": "Régie démo"}}}
	if e.Dossier {
		m["folder"] = map[string]int{}
	}
	return m
}

func (s *sharePointFactice) parChemin(chemin string) *elementFactice {
	cur := s.elts["root"]
	for _, seg := range strings.Split(chemin, "/") {
		var trouve *elementFactice
		for _, e := range s.elts {
			if e.Parent == cur.ID && e.Nom == seg {
				trouve = e
			}
		}
		if trouve == nil {
			return nil
		}
		cur = trouve
	}
	return cur
}

func (s *sharePointFactice) creer(parent, nom string, dossier bool, renommer bool) *elementFactice {
	base, n := nom, 1
	for {
		libre := true
		for _, e := range s.elts {
			if e.Parent == parent && e.Nom == nom {
				libre = false
			}
		}
		if libre || !renommer {
			break
		}
		n++
		nom = fmt.Sprintf("%s %d", base, n)
	}
	e := &elementFactice{ID: s.id(), Nom: nom, Parent: parent, Dossier: dossier, Modif: time.Now(), Perms: map[string]string{}}
	s.elts[e.ID] = e
	return e
}

func (s *sharePointFactice) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := r.URL.Path
	ecrire := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
	absent := func() {
		w.WriteHeader(404)
		ecrire(map[string]any{"error": map[string]string{"code": "itemNotFound", "message": "absent"}})
	}

	switch {
	case strings.HasPrefix(p, "/sites/") && !strings.HasSuffix(p, "/drive"):
		ecrire(map[string]string{"id": "site"})
	case strings.HasSuffix(p, "/drive"):
		ecrire(map[string]string{"id": "drv"})
	case strings.HasPrefix(p, "/drives/drv/root:/"):
		chemin := strings.TrimPrefix(p, "/drives/drv/root:/")
		if r.Method == "PUT" {
			chemin = strings.TrimSuffix(chemin, ":/content")
			dir, nom := chemin[:strings.LastIndex(chemin, "/")], chemin[strings.LastIndex(chemin, "/")+1:]
			parent := s.parChemin(dir)
			e := s.parChemin(chemin)
			b, _ := io.ReadAll(r.Body)
			if e == nil {
				if r.Header.Get("If-Match") != "" {
					w.WriteHeader(412)
					return
				}
				e = s.creer(parent.ID, nom, false, false)
			} else if r.Header.Get("If-None-Match") == "*" || r.Header.Get("If-Match") != s.json(e)["eTag"] {
				w.WriteHeader(412)
				return
			}
			e.Contenu, e.Version, e.Modif = b, e.Version+1, time.Now()
			ecrire(s.json(e))
			return
		}
		if e := s.parChemin(chemin); e != nil {
			ecrire(s.json(e))
		} else {
			absent()
		}
	case p == "/drives/drv/root/children":
		var c struct{ Name string }
		_ = json.NewDecoder(r.Body).Decode(&c)
		ecrire(s.json(s.creer("root", c.Name, true, false)))
	case strings.HasPrefix(p, "/drives/drv/items/"):
		reste := strings.TrimPrefix(p, "/drives/drv/items/")
		id, suite, _ := strings.Cut(reste, "/")
		if i := strings.Index(id, ":"); i >= 0 { // items/{parent}:/{nom}:/content
			parent, nom := id[:i], strings.TrimSuffix(strings.TrimPrefix(reste[i:], ":/"), ":/content")
			e := s.creer(parent, nom, false, true)
			e.Contenu, _ = io.ReadAll(r.Body)
			ecrire(s.json(e))
			return
		}
		e := s.elts[id]
		if e == nil {
			absent()
			return
		}
		switch {
		case suite == "children" && r.Method == "POST":
			var c struct{ Name string }
			_ = json.NewDecoder(r.Body).Decode(&c)
			ecrire(s.json(s.creer(id, c.Name, true, true)))
		case suite == "children":
			var v []any
			for _, x := range s.elts {
				if x.Parent == id {
					v = append(v, s.json(x))
				}
			}
			ecrire(map[string]any{"value": v})
		case suite == "content":
			_, _ = w.Write(e.Contenu)
		case suite == "invite":
			pid := s.id()
			var c struct{ Recipients []struct{ Email string } }
			_ = json.NewDecoder(r.Body).Decode(&c)
			e.Perms[pid] = c.Recipients[0].Email
			ecrire(map[string]any{"value": []map[string]string{{"id": "perm_" + pid}}})
		case strings.HasPrefix(suite, "permissions/"):
			w.WriteHeader(204)
		case r.Method == "DELETE":
			delete(s.elts, id)
			w.WriteHeader(204)
		case r.Method == "PATCH":
			var c struct{ Name string }
			_ = json.NewDecoder(r.Body).Decode(&c)
			e.Nom = c.Name
			ecrire(s.json(e))
		default:
			ecrire(s.json(e))
		}
	default:
		absent()
	}
}
