package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

// stockageMemoire simule le fichier SharePoint avec son eTag.
type stockageMemoire struct {
	mu      sync.Mutex
	contenu []byte
	version int
	// avantEcriture permet de simuler un autre poste qui écrit entre-temps.
	avantEcriture func()
}

func (s *stockageMemoire) Lire(context.Context) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.contenu == nil {
		return nil, "", ErrIntrouvable
	}
	return s.contenu, etagDe(s.version), nil
}

func (s *stockageMemoire) Ecrire(_ context.Context, b []byte, etag string) error {
	if f := s.avantEcriture; f != nil {
		s.avantEcriture = nil
		f()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if (s.contenu == nil && etag != "") || (s.contenu != nil && etag != etagDe(s.version)) {
		return ErrConflit
	}
	s.contenu, s.version = b, s.version+1
	return nil
}

func etagDe(v int) string { return string(rune('a' + v)) }

func TestModifierRejoueApresConflit(t *testing.T) {
	ctx := context.Background()
	m := &stockageMemoire{}
	s := &Store{stock: m}
	if _, err := s.Modifier(ctx, func(d *Donnees) error {
		d.Regisseurs = append(d.Regisseurs, Regisseur{ID: "reg_a", Nom: "A"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Un autre poste ajoute B pendant que ce poste ajoute C.
	m.avantEcriture = func() {
		autre := &Store{stock: m}
		if _, err := autre.Modifier(ctx, func(d *Donnees) error {
			d.Regisseurs = append(d.Regisseurs, Regisseur{ID: "reg_b", Nom: "B"})
			return nil
		}); err != nil {
			t.Error(err)
		}
	}
	appels := 0
	d, err := s.Modifier(ctx, func(d *Donnees) error {
		appels++
		d.Regisseurs = append(d.Regisseurs, Regisseur{ID: "reg_c", Nom: "C"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if appels != 2 {
		t.Errorf("modification appliquée %d fois, attendu 2 (rejeu après conflit)", appels)
	}
	if len(d.Regisseurs) != 3 {
		t.Fatalf("attendu A, B et C, obtenu %+v", d.Regisseurs)
	}
	var relu Donnees
	_ = json.Unmarshal(m.contenu, &relu)
	if len(relu.Regisseurs) != 3 {
		t.Errorf("fichier enregistré incomplet : %+v", relu.Regisseurs)
	}
}

func TestModifierErreurNEcritPas(t *testing.T) {
	m := &stockageMemoire{}
	s := &Store{stock: m}
	_, err := s.Modifier(context.Background(), func(*Donnees) error { return errors.New("non") })
	if err == nil || m.contenu != nil {
		t.Fatal("une modification refusée ne doit rien écrire")
	}
}

func TestNormaliserRegisseur(t *testing.T) {
	r := Regisseur{Prenom: " Léa ", Email: " Lea.Martin@Exemple.FR "}
	if err := r.Normaliser(); err != nil {
		t.Fatal(err)
	}
	if r.Email != "lea.martin@exemple.fr" || r.Prenom != "Léa" {
		t.Errorf("normalisation : %+v", r)
	}
	for _, mauvais := range []string{"", "lea", "Léa <lea@x.fr>", "lea@x.fr, paul@x.fr"} {
		r := Regisseur{Nom: "X", Email: mauvais}
		if r.Normaliser() == nil {
			t.Errorf("adresse %q acceptée", mauvais)
		}
	}
}

func TestNormaliserProjet(t *testing.T) {
	p := Projet{Nom: "X", DateDebut: "2026-11-10", DateFin: "2026-11-01"}
	if p.Normaliser() == nil {
		t.Error("fin avant début acceptée")
	}
	p = Projet{Nom: "X", DateDebut: "10/11/2026"}
	if p.Normaliser() == nil {
		t.Error("date mal formée acceptée")
	}
}

func TestNomDossier(t *testing.T) {
	cas := map[string]Projet{
		"2026-11 La demande en mariage": {Nom: "La demande en mariage", DateDebut: "2026-11-14"},
		"Concert - AC-DC":               {Nom: "Concert : AC/DC"},
		"Projet":                        {Nom: "..."},
	}
	for attendu, p := range cas {
		if got := NomDossier(p); got != attendu {
			t.Errorf("NomDossier(%q) = %q, attendu %q", p.Nom, got, attendu)
		}
	}
}
