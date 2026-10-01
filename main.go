// Régie Partage : partage de documents de projets avec les régisseurs,
// via le SharePoint (ou le OneDrive) Microsoft 365 de la salle.
//
// Un seul exécutable : serveur local sur 127.0.0.1 + interface web embarquée.
// Les données métier (régisseurs, projets, accès) vivent dans un fichier JSON
// du dossier racine sur SharePoint, partagé entre tous les postes de la salle.
// Seuls la configuration et le jeton de connexion restent sur le poste.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"time"
)

// Port fixe : l'adresse de retour de connexion déclarée dans Microsoft Entra
// (http://localhost:47820/auth/retour) doit correspondre exactement.
const port = 47820

var version = "dev"

//go:embed web
var webFS embed.FS

func main() {
	sansNavigateur := flag.Bool("sans-navigateur", false, "ne pas ouvrir le navigateur au démarrage")
	flag.Parse()

	adresse := fmt.Sprintf("http://localhost:%d/", port)

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		// Déjà lancée (double-clic de trop) : on rouvre simplement la page.
		fmt.Println("Régie Partage est déjà ouverte :", adresse)
		ouvrirNavigateur(adresse)
		time.Sleep(2 * time.Second)
		return
	}

	dir, err := dossierLocal()
	if err != nil {
		log.Fatal(err)
	}
	app, err := NouvelleApp(dir, fmt.Sprintf("http://localhost:%d/auth/retour", port))
	if err != nil {
		log.Fatal(err)
	}

	web, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	app.Routes(mux)
	mux.Handle("/", http.FileServer(http.FS(web)))

	srv := &http.Server{Handler: verifierHote(mux), ReadHeaderTimeout: 10 * time.Second}
	app.quitter = func() {
		go func() {
			time.Sleep(300 * time.Millisecond)
			_ = srv.Shutdown(context.Background())
		}()
	}

	fmt.Printf("Régie Partage %s\n", version)
	fmt.Println("Ouverte dans le navigateur :", adresse)
	fmt.Println("Laisser cette fenêtre ouverte ; la fermer quitte l'application.")
	if !*sansNavigateur {
		ouvrirNavigateur(adresse)
	}

	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		<-c
		_ = srv.Shutdown(context.Background())
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// verifierHote refuse les requêtes dont l'en-tête Host n'est pas local
// (protection contre le « DNS rebinding » depuis un site malveillant).
func verifierHote(h http.Handler) http.Handler {
	ok := map[string]bool{
		fmt.Sprintf("localhost:%d", port): true,
		fmt.Sprintf("127.0.0.1:%d", port): true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ok[r.Host] {
			http.Error(w, "hôte refusé", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// dossierLocal : %AppData%\RegiePartage sous Windows.
func dossierLocal() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := base + string(os.PathSeparator) + "RegiePartage"
	return dir, os.MkdirAll(dir, 0o700)
}

func ouvrirNavigateur(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
