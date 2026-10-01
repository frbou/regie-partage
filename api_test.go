package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Deux invités extérieurs regroupés par SharePoint sur le même lien : retirer
// l'un ne doit retirer que lui (revokeGrants), pas supprimer le lien commun.
func TestRetirerSurLienCommun(t *testing.T) {
	var appels []string
	g := grapheFactice(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/drive"):
			fmt.Fprint(w, `{"id":"drv"}`)
		case strings.HasPrefix(r.URL.Path, "/sites/"):
			fmt.Fprint(w, `{"id":"site1"}`)
		default:
			appels = append(appels, r.Method+" "+r.URL.Path)
			w.WriteHeader(204)
		}
	})
	app := &App{auth: g.auth, graphe: g}
	d := &Donnees{
		Regisseurs: []Regisseur{{ID: "a", Email: "a@x.fr"}, {ID: "b", Email: "b@x.fr"}},
		Projets: []Projet{{ID: "p", DossierID: "dos", Acces: []Acces{
			{RegisseurID: "a", PermissionID: "lien1"}, {RegisseurID: "b", PermissionID: "lien1"}}}},
	}
	ctx := context.Background()
	if err := app.retirer(ctx, d, &d.Projets[0], "a"); err != nil {
		t.Fatal(err)
	}
	if len(appels) != 1 || appels[0] != "POST /drives/drv/items/dos/permissions/lien1/revokeGrants" {
		t.Fatalf("lien commun : appels %v", appels)
	}

	appels = nil
	d.Projets[0].Acces = d.Projets[0].Acces[1:]
	if err := app.retirer(ctx, d, &d.Projets[0], "b"); err != nil {
		t.Fatal(err)
	}
	if len(appels) != 1 || appels[0] != "DELETE /drives/drv/items/dos/permissions/lien1" {
		t.Fatalf("dernier invité : appels %v", appels)
	}
}
