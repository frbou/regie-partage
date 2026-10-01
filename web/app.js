"use strict";

// Régie Partage — interface (JavaScript sans dépendance).

const vue = document.getElementById("vue");
const dialogue = document.getElementById("dialogue");
let etat = null;      // réglages + compte
let donnees = null;   // régisseurs + projets

// --- Utilitaires ---

const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

async function api(methode, chemin, corps) {
  const opts = { method: methode, headers: { "X-Regie": "1" } };
  if (corps !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(corps);
  }
  let r;
  try {
    r = await fetch(chemin, opts);
  } catch {
    throw new Error("Régie Partage ne répond plus : relancer l'application.");
  }
  const json = await r.json().catch(() => ({}));
  if (r.status === 401) {
    etat.compte = null;
    afficher();
    throw new Error("Connexion Microsoft 365 expirée : se reconnecter.");
  }
  if (!r.ok) throw new Error(json.erreur || "Erreur " + r.status);
  return json;
}

function toast(msg, erreur) {
  const t = document.createElement("div");
  t.className = "toast" + (erreur ? " erreur" : "");
  t.textContent = msg;
  document.getElementById("toasts").append(t);
  setTimeout(() => t.remove(), erreur ? 8000 : 3500);
}

// Exécute une action en désactivant le bouton et en signalant les erreurs.
async function action(bouton, f, succes) {
  if (bouton) bouton.disabled = true;
  try {
    const r = await f();
    if (succes) toast(succes);
    return r;
  } catch (e) {
    toast(e.message, true);
  } finally {
    if (bouton) bouton.disabled = false;
  }
}

const fmtDate = (iso) => iso ? new Date(iso + (iso.length === 10 ? "T12:00:00" : "")).toLocaleDateString("fr-FR", { day: "numeric", month: "short", year: "numeric" }) : "";
const fmtDateHeure = (iso) => iso ? new Date(iso).toLocaleString("fr-FR", { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" }) : "";
function fmtPeriode(p) {
  if (!p.dateDebut) return "Dates à préciser";
  if (!p.dateFin || p.dateFin === p.dateDebut) return fmtDate(p.dateDebut);
  return fmtDate(p.dateDebut) + " → " + fmtDate(p.dateFin);
}
function fmtTaille(o) {
  if (o < 1024) return o + " o";
  if (o < 1 << 20) return Math.round(o / 1024) + " Ko";
  return (o / (1 << 20)).toFixed(1).replace(".", ",") + " Mo";
}
const nomComplet = (r) => [r.prenom, r.nom].filter(Boolean).join(" ");
const extension = (nom) => (nom.includes(".") ? nom.split(".").pop().slice(0, 4).toUpperCase() : "—");

function ouvrirDialogue(html, initialiser) {
  dialogue.innerHTML = html;
  dialogue.showModal();
  dialogue.querySelectorAll("[data-fermer]").forEach((b) => (b.onclick = () => dialogue.close()));
  initialiser?.(dialogue);
  dialogue.querySelector("input, textarea, select")?.focus();
}

function confirmer(titre, texte, libelle = "Confirmer") {
  return new Promise((resoudre) => {
    ouvrirDialogue(`<h2>${esc(titre)}</h2><p>${esc(texte)}</p>
      <div class="pied"><button data-fermer>Annuler</button><button class="principal danger" id="ok">${esc(libelle)}</button></div>`,
      (d) => {
        d.querySelector("#ok").onclick = () => { d.close(); resoudre(true); };
        d.addEventListener("close", () => resoudre(false), { once: true });
      });
  });
}

// --- Navigation ---

window.addEventListener("hashchange", afficher);

async function demarrer() {
  try {
    etat = await api("GET", "/api/etat");
  } catch (e) {
    vue.innerHTML = `<p class="carte">${esc(e.message)}</p>`;
    return;
  }
  afficher();
}

async function afficher() {
  const [, page = "projets", id] = location.hash.split("/");
  document.querySelectorAll("[data-nav]").forEach((a) =>
    a.classList.toggle("actif", a.dataset.nav === (page === "projet" ? "projets" : page)));
  document.getElementById("compte").textContent = etat?.compte ? etat.compte.nom : "";

  if (!etat.configComplete || page === "reglages") return pageReglages();
  if (!etat.compte) return pageConnexion();

  if (!donnees) {
    vue.innerHTML = `<p class="attente">Chargement depuis Microsoft 365…</p>`;
    try {
      donnees = await api("GET", "/api/donnees");
    } catch (e) {
      vue.innerHTML = `<div class="carte"><h2>Impossible de lire les données</h2><p>${esc(e.message)}</p>
        <button onclick="location.reload()">Réessayer</button></div>`;
      return;
    }
  }
  if (page === "regisseurs") return pageRegisseurs();
  if (page === "projet" && id) return pageProjet(id);
  pageProjets();
}

async function recharger() {
  donnees = await api("GET", "/api/donnees");
}

function pageConnexion() {
  vue.innerHTML = `<div class="carte bloc-connexion">
    <h1>Bienvenue</h1>
    <p class="doux">Connectez-vous avec le compte Microsoft 365 de la salle pour accéder aux projets.</p>
    <p><a class="bouton principal" href="/auth/connexion">Se connecter à Microsoft 365</a></p></div>`;
}

// --- Projets ---

let voirArchives = false;

function pageProjets() {
  const projets = donnees.projets.filter((p) => voirArchives || !p.archive);
  const nbArchives = donnees.projets.filter((p) => p.archive).length;
  vue.innerHTML = `
    <div class="entete">
      <div><h1>Projets</h1><p class="doux">Un dossier SharePoint par projet, partagé avec ses régisseurs.</p></div>
      <div class="actions">
        ${nbArchives ? `<button id="archives">${voirArchives ? "Masquer" : "Afficher"} les archives (${nbArchives})</button>` : ""}
        <button class="principal" id="nouveau">+ Nouveau projet</button>
      </div>
    </div>
    ${projets.length ? `<div class="grille">${projets.map((p) => `
      <a class="carte projet ${p.archive ? "archive" : ""}" href="#/projet/${esc(p.id)}">
        <h2>${esc(p.nom)}</h2>
        <div class="doux petit">${esc(fmtPeriode(p))}${p.archive ? " · archivé" : ""}</div>
        <div class="petit" style="margin-top:.6rem">${p.acces?.length ? `${p.acces.length} régisseur${p.acces.length > 1 ? "s" : ""} : ${esc(p.acces.map((a) => nomComplet(donnees.regisseurs.find((r) => r.id === a.regisseurId) || {})).join(", "))}` : `<span class="doux">Aucun régisseur invité</span>`}</div>
      </a>`).join("")}</div>`
    : `<div class="carte vide">Aucun projet pour l'instant.<br><br><button class="principal" onclick="formProjet()">Créer le premier projet</button></div>`}`;
  document.getElementById("nouveau").onclick = () => formProjet();
  const b = document.getElementById("archives");
  if (b) b.onclick = () => { voirArchives = !voirArchives; pageProjets(); };
}

function formProjet(p = {}) {
  ouvrirDialogue(`<form method="dialog" id="f">
    <h2>${p.id ? "Modifier le projet" : "Nouveau projet"}</h2>
    <label for="nom">Nom du projet</label><input id="nom" required value="${esc(p.nom)}" placeholder="ex. La demande en mariage">
    <div class="ligne-champs">
      <div><label for="debut">Début</label><input id="debut" type="date" value="${esc(p.dateDebut)}"></div>
      <div><label for="fin">Fin</label><input id="fin" type="date" value="${esc(p.dateFin)}"></div>
    </div>
    <label for="notes">Notes (internes, non partagées)</label><textarea id="notes" style="min-height:4rem">${esc(p.notes)}</textarea>
    ${p.id ? `<label class="case"><input type="checkbox" id="archive" ${p.archive ? "checked" : ""}> Projet archivé (les accès sont conservés)</label>` : ""}
    <div class="pied"><button type="button" data-fermer>Annuler</button><button class="principal" id="ok">${p.id ? "Enregistrer" : "Créer le projet"}</button></div>
  </form>`, (d) => {
    d.querySelector("#debut").onchange = (e) => { const fin = d.querySelector("#fin"); if (!fin.value || fin.value < e.target.value) fin.value = e.target.value; };
    d.querySelector("#f").onsubmit = async (e) => {
      e.preventDefault();
      const corps = {
        ...p, nom: d.querySelector("#nom").value, dateDebut: d.querySelector("#debut").value,
        dateFin: d.querySelector("#fin").value, notes: d.querySelector("#notes").value,
        archive: d.querySelector("#archive")?.checked ?? false,
      };
      const r = await action(d.querySelector("#ok"), () => api("POST", "/api/projets", corps), p.id ? "Projet enregistré" : "Projet et dossier SharePoint créés");
      if (!r) return;
      donnees = r;
      d.close();
      if (!p.id) {
        const dernier = [...r.projets].sort((a, b) => (b.creeLe || "").localeCompare(a.creeLe || ""))[0];
        location.hash = "#/projet/" + dernier.id;
      } else afficher();
    };
  });
}

// --- Fiche projet ---

async function pageProjet(id) {
  const p = donnees.projets.find((x) => x.id === id);
  if (!p) { location.hash = "#/projets"; return; }
  const invites = (p.acces || []).map((a) => ({ a, r: donnees.regisseurs.find((r) => r.id === a.regisseurId) })).filter((x) => x.r);
  const disponibles = donnees.regisseurs.filter((r) => !invites.some((x) => x.r.id === r.id));

  vue.innerHTML = `
    <div class="entete">
      <div>
        <p class="petit"><a href="#/projets">← Projets</a></p>
        <h1>${esc(p.nom)}</h1>
        <p class="doux">${esc(fmtPeriode(p))}${p.archive ? " · archivé" : ""}</p>
      </div>
      <div class="actions">
        ${p.webUrl ? `<a class="bouton" href="${esc(p.webUrl)}" target="_blank" rel="noopener">Ouvrir dans SharePoint ↗</a>` : ""}
        <button id="modifier">Modifier</button>
        <button class="danger" id="supprimer">Supprimer</button>
      </div>
    </div>
    ${p.notes ? `<p class="carte doux" style="white-space:pre-wrap">${esc(p.notes)}</p><br>` : ""}
    <div class="deux">
      <section class="carte">
        <h2>Documents</h2>
        <div class="depot" id="depot">Glisser des fichiers ici ou <u>cliquer pour choisir</u>
          <input type="file" id="fichiers" multiple hidden></div>
        <div id="envois"></div>
        <div id="docs"><p class="attente">Chargement des documents…</p></div>
      </section>
      <section class="carte">
        <h2>Régisseurs invités</h2>
        <div id="invites">${invites.length ? invites.map(({ a, r }) => `
          <div class="personne">
            <div class="qui"><strong>${esc(nomComplet(r))}</strong>
              <div class="doux petit">${esc(r.metier || r.email)} · envoyé le ${esc(fmtDateHeure(a.envoyeLe))}</div></div>
            <span class="pastille ${a.ecriture ? "ecriture" : ""}">${a.ecriture ? "Lecture + dépôt" : "Lecture"}</span>
            <button class="lien" title="Options" data-options="${esc(r.id)}">•••</button>
          </div>`).join("") : `<p class="doux">Personne n'a encore accès à ce dossier.</p>`}</div>
        <hr style="border:none;border-top:1px solid var(--ligne);margin:1rem 0">
        ${donnees.regisseurs.length === 0
          ? `<p class="doux">Ajoutez d'abord vos régisseurs dans l'onglet <a href="#/regisseurs">Régisseurs</a>.</p>`
          : disponibles.length === 0 ? `<p class="doux petit">Tous les régisseurs ont accès à ce projet.</p>`
          : `<h2>Donner accès</h2>
            <label for="qui">Régisseur</label>
            <select id="qui">${disponibles.map((r) => `<option value="${esc(r.id)}">${esc(nomComplet(r))}${r.metier ? " — " + esc(r.metier) : ""}</option>`).join("")}</select>
            <label for="droit">Droit</label>
            <select id="droit"><option value="0">Lecture seule</option><option value="1">Lecture + dépôt de fichiers</option></select>
            <p style="margin-top:.9rem"><button class="principal" id="inviter">Envoyer l'accès par e-mail</button>
            <button class="lien" id="perso">Personnaliser le message…</button></p>`}
      </section>
    </div>`;

  document.getElementById("modifier").onclick = () => formProjet(p);
  document.getElementById("supprimer").onclick = async (e) => {
    if (!(await confirmer("Supprimer le projet ?", `Tous les accès des régisseurs à « ${p.nom} » seront retirés. Le dossier et ses documents restent sur SharePoint.`, "Supprimer"))) return;
    const r = await action(e.target, () => api("DELETE", "/api/projets/" + p.id), "Projet supprimé");
    if (r) { donnees = r; location.hash = "#/projets"; }
  };

  document.querySelectorAll("[data-options]").forEach((b) => (b.onclick = () => optionsAcces(p, b.dataset.options)));
  const bInviter = document.getElementById("inviter");
  if (bInviter) {
    bInviter.onclick = () => inviter(p, bInviter, null);
    document.getElementById("perso").onclick = async () => {
      const regId = document.getElementById("qui").value;
      const m = await action(null, () => api("GET", `/api/projets/${p.id}/message/${regId}`));
      if (!m) return;
      ouvrirDialogue(`<h2>Message d'invitation</h2>
        <p class="doux petit">Joint à l'e-mail envoyé par Microsoft avec le lien d'accès.</p>
        <textarea id="msg" style="min-height:12rem">${esc(m.message)}</textarea>
        <div class="pied"><button data-fermer>Annuler</button><button class="principal" id="ok">Envoyer l'accès</button></div>`,
        (d) => (d.querySelector("#ok").onclick = async (e) => { if (await inviter(p, e.target, d.querySelector("#msg").value)) d.close(); }));
    };
  }

  // Dépôt de fichiers
  const depot = document.getElementById("depot");
  const champ = document.getElementById("fichiers");
  depot.onclick = () => champ.click();
  champ.onchange = () => { envoyerFichiers(p, [...champ.files]); champ.value = ""; };
  depot.ondragover = (e) => { e.preventDefault(); depot.classList.add("survol"); };
  depot.ondragleave = () => depot.classList.remove("survol");
  depot.ondrop = (e) => { e.preventDefault(); depot.classList.remove("survol"); envoyerFichiers(p, [...e.dataTransfer.files]); };

  chargerDocuments(p);
}

async function inviter(p, bouton, message) {
  const regisseurId = document.getElementById("qui").value;
  const ecriture = document.getElementById("droit").value === "1";
  const r = donnees.regisseurs.find((x) => x.id === regisseurId);
  const res = await action(bouton, () => api("POST", `/api/projets/${p.id}/acces`, { regisseurId, ecriture, message: message || "" }),
    `Accès envoyé à ${nomComplet(r)} (${r.email})`);
  if (res) { donnees = res; pageProjet(p.id); }
  return !!res;
}

function optionsAcces(p, regId) {
  const r = donnees.regisseurs.find((x) => x.id === regId);
  const a = p.acces.find((x) => x.regisseurId === regId);
  ouvrirDialogue(`<h2>${esc(nomComplet(r))}</h2>
    <p class="doux">${esc(r.email)}<br>Accès ${a.ecriture ? "lecture + dépôt" : "lecture seule"}, envoyé le ${esc(fmtDateHeure(a.envoyeLe))}.</p>
    <p><button id="renvoyer">Renvoyer l'e-mail d'accès</button></p>
    <p><button id="droit">Passer en ${a.ecriture ? "lecture seule" : "lecture + dépôt"}</button>
      <span class="doux petit">(un nouvel e-mail est envoyé)</span></p>
    <p><button class="danger" id="retirer">Retirer l'accès</button></p>
    <div class="pied"><button data-fermer>Fermer</button></div>`, (d) => {
    const fin = (res) => { if (res) { donnees = res; d.close(); pageProjet(p.id); } };
    d.querySelector("#renvoyer").onclick = async (e) => fin(await action(e.target, () => api("POST", `/api/projets/${p.id}/acces/${regId}/renvoyer`), "E-mail renvoyé"));
    d.querySelector("#droit").onclick = async (e) => fin(await action(e.target, () => api("POST", `/api/projets/${p.id}/acces`, { regisseurId: regId, ecriture: !a.ecriture, message: "" }), "Droit modifié"));
    d.querySelector("#retirer").onclick = async (e) => fin(await action(e.target, () => api("DELETE", `/api/projets/${p.id}/acces/${regId}`), "Accès retiré"));
  });
}

async function chargerDocuments(p) {
  const zone = document.getElementById("docs");
  let docs;
  try {
    docs = await api("GET", `/api/projets/${p.id}/documents`);
  } catch (e) {
    if (zone) zone.innerHTML = `<p class="doux">${esc(e.message)}</p>`;
    return;
  }
  if (!document.getElementById("docs")) return; // page quittée entre-temps
  docs.sort((a, b) => (!!b.folder - !!a.folder) || a.name.localeCompare(b.name, "fr"));
  zone.innerHTML = docs.length ? docs.map((d) => `
    <div class="doc">
      <span class="ico">${d.folder ? "DOS" : esc(extension(d.name))}</span>
      <div class="nom"><a href="${esc(d.webUrl)}" target="_blank" rel="noopener">${esc(d.name)}</a>
        <div class="doux petit">${d.folder ? "Dossier" : esc(fmtTaille(d.size))} · ${esc(fmtDateHeure(d.lastModifiedDateTime))}${d.lastModifiedBy?.user?.displayName ? " · " + esc(d.lastModifiedBy.user.displayName) : ""}</div></div>
      ${d.folder ? "" : `<button class="lien" title="Supprimer" data-suppr="${esc(d.id)}" data-nom="${esc(d.name)}">✕</button>`}
    </div>`).join("") : `<p class="doux">Aucun document. Les fichiers déposés ici seront visibles par les régisseurs invités.</p>`;
  zone.querySelectorAll("[data-suppr]").forEach((b) => (b.onclick = async () => {
    if (!(await confirmer("Supprimer ce document ?", `« ${b.dataset.nom} » ira dans la corbeille SharePoint (récupérable pendant 93 jours).`, "Supprimer"))) return;
    if (await action(b, () => api("DELETE", `/api/projets/${p.id}/documents/${b.dataset.suppr}`), "Document supprimé")) chargerDocuments(p);
  }));
}

function envoyerFichiers(p, fichiers) {
  const zone = document.getElementById("envois");
  const fichiersValides = fichiers.filter((f) => {
    if (f.size === 0) { toast(`« ${f.name} » est vide ou est un dossier : ignoré.`, true); return false; }
    return true;
  });
  let restants = fichiersValides.length;
  // Envois les uns après les autres pour ne pas saturer la connexion.
  let chaine = Promise.resolve();
  for (const f of fichiersValides) {
    const ligne = document.createElement("div");
    ligne.className = "petit";
    ligne.innerHTML = `${esc(f.name)} <span class="doux">(${esc(fmtTaille(f.size))})</span><div class="progression"><div></div></div>`;
    zone.append(ligne);
    chaine = chaine.then(() => new Promise((fini) => {
      const xhr = new XMLHttpRequest();
      xhr.open("PUT", `/api/projets/${p.id}/documents?nom=${encodeURIComponent(f.name)}`);
      xhr.setRequestHeader("X-Regie", "1");
      xhr.upload.onprogress = (e) => { if (e.lengthComputable) ligne.querySelector(".progression div").style.width = (95 * e.loaded / e.total) + "%"; };
      xhr.onloadend = () => {
        if (xhr.status === 200) {
          ligne.remove();
        } else {
          let msg = "envoi impossible";
          try { msg = JSON.parse(xhr.responseText).erreur || msg; } catch {}
          ligne.remove();
          toast(`« ${f.name} » : ${msg}`, true);
        }
        if (--restants === 0) { toast(fichiersValides.length > 1 ? "Fichiers envoyés" : "Fichier envoyé"); chargerDocuments(p); }
        fini();
      };
      xhr.send(f);
    }));
  }
}

// --- Régisseurs ---

let filtre = "";

function pageRegisseurs() {
  const f = filtre.toLowerCase();
  const liste = donnees.regisseurs.filter((r) => !f || [r.prenom, r.nom, r.email, r.metier].join(" ").toLowerCase().includes(f));
  const nbProjets = (id) => donnees.projets.filter((p) => !p.archive && p.acces?.some((a) => a.regisseurId === id)).length;
  vue.innerHTML = `
    <div class="entete">
      <div><h1>Régisseurs</h1><p class="doux">${donnees.regisseurs.length} personne${donnees.regisseurs.length > 1 ? "s" : ""} dans l'annuaire.</p></div>
      <div class="actions"><input id="filtre" placeholder="Rechercher…" value="${esc(filtre)}" style="width:14rem">
        <button class="principal" id="nouveau">+ Ajouter</button></div>
    </div>
    <div class="carte">${liste.length ? `<table>
      <thead><tr><th>Nom</th><th>Métier</th><th>E-mail</th><th>Téléphone</th><th>Projets en cours</th><th></th></tr></thead>
      <tbody>${liste.map((r) => `<tr>
        <td><strong>${esc(nomComplet(r))}</strong></td><td>${esc(r.metier)}</td>
        <td><a href="mailto:${esc(r.email)}">${esc(r.email)}</a></td><td>${esc(r.telephone)}</td>
        <td>${nbProjets(r.id) || ""}</td>
        <td class="actions"><button class="lien" data-modif="${esc(r.id)}">Modifier</button></td></tr>`).join("")}</tbody></table>`
      : `<p class="vide">${donnees.regisseurs.length ? "Aucun résultat." : "Aucun régisseur. Ajoutez les personnes avec qui partager des documents."}</p>`}</div>`;
  const champ = document.getElementById("filtre");
  champ.oninput = () => { filtre = champ.value; pageRegisseurs(); const c = document.getElementById("filtre"); c.focus(); c.setSelectionRange(c.value.length, c.value.length); };
  document.getElementById("nouveau").onclick = () => formRegisseur();
  document.querySelectorAll("[data-modif]").forEach((b) => (b.onclick = () => formRegisseur(donnees.regisseurs.find((r) => r.id === b.dataset.modif))));
}

const METIERS = ["Régie générale", "Régie lumière", "Régie son", "Régie plateau", "Régie vidéo", "Machinerie", "Électricien·ne", "Habillage"];

function formRegisseur(r = {}) {
  ouvrirDialogue(`<form method="dialog" id="f">
    <h2>${r.id ? "Modifier" : "Nouveau régisseur"}</h2>
    <div class="ligne-champs">
      <div><label for="prenom">Prénom</label><input id="prenom" value="${esc(r.prenom)}"></div>
      <div><label for="nom">Nom</label><input id="nom" value="${esc(r.nom)}"></div>
    </div>
    <label for="email">E-mail (reçoit les accès)</label><input id="email" type="email" required value="${esc(r.email)}">
    <div class="ligne-champs">
      <div><label for="metier">Métier</label><input id="metier" list="metiers" value="${esc(r.metier)}">
        <datalist id="metiers">${METIERS.map((m) => `<option value="${esc(m)}">`).join("")}</datalist></div>
      <div><label for="tel">Téléphone</label><input id="tel" type="tel" value="${esc(r.telephone)}"></div>
    </div>
    <label for="notes">Notes</label><textarea id="notes" style="min-height:3.5rem">${esc(r.notes)}</textarea>
    <div class="pied">
      ${r.id ? `<button type="button" class="danger" id="suppr" style="margin-right:auto">Supprimer</button>` : ""}
      <button type="button" data-fermer>Annuler</button><button class="principal" id="ok">Enregistrer</button></div>
  </form>`, (d) => {
    d.querySelector("#f").onsubmit = async (e) => {
      e.preventDefault();
      const corps = { ...r, prenom: d.querySelector("#prenom").value, nom: d.querySelector("#nom").value,
        email: d.querySelector("#email").value, metier: d.querySelector("#metier").value,
        telephone: d.querySelector("#tel").value, notes: d.querySelector("#notes").value };
      const res = await action(d.querySelector("#ok"), () => api("POST", "/api/regisseurs", corps), "Régisseur enregistré");
      if (res) { donnees = res; d.close(); afficher(); }
    };
    const s = d.querySelector("#suppr");
    if (s) s.onclick = async () => {
      d.close();
      if (!(await confirmer("Supprimer ce régisseur ?", `${nomComplet(r)} perdra l'accès à tous ses projets.`, "Supprimer"))) return;
      const res = await action(null, () => api("DELETE", "/api/regisseurs/" + r.id), "Régisseur supprimé");
      if (res) { donnees = res; afficher(); }
    };
  });
}

// --- Réglages ---

function pageReglages() {
  const c = etat.config;
  vue.innerHTML = `
    <div class="entete"><div><h1>Réglages</h1><p class="doux">Version ${esc(etat.version)}</p></div></div>
    <div class="deux">
      <form class="carte" id="f">
        <h2>Microsoft 365 de la salle</h2>
        <p class="doux petit">Valeurs fournies par l'administrateur Microsoft 365 (voir le guide administrateur livré avec l'application).</p>
        <label for="tenant">ID de l'annuaire (tenant)</label><input id="tenant" required value="${esc(c.tenantId)}" placeholder="xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx">
        <label for="client">ID de l'application (client)</label><input id="client" required value="${esc(c.clientId)}" placeholder="xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx">
        <label for="site">Adresse du site SharePoint</label><input id="site" value="${esc(c.siteUrl)}" placeholder="https://masalle.sharepoint.com/sites/Regie">
        <p class="doux petit">Vide = OneDrive de la personne connectée (déconseillé : les dossiers disparaissent si elle quitte la salle).</p>
        <label for="racine">Dossier contenant les projets</label><input id="racine" value="${esc(c.dossierRacine)}">
        <label for="salle">Signature des invitations</label><input id="salle" value="${esc(c.nomSalle)}" placeholder="ex. La régie technique du Théâtre…">
        <p style="margin-top:1rem"><button class="principal" id="ok">Enregistrer</button></p>
      </form>
      <section class="carte">
        <h2>Compte</h2>
        ${etat.compte ? `<p>Connecté : <strong>${esc(etat.compte.nom)}</strong><br><span class="doux">${esc(etat.compte.email)}</span></p>
          <p><button id="deco">Se déconnecter</button></p>`
        : etat.configComplete ? `<p><a class="bouton principal" href="/auth/connexion">Se connecter à Microsoft 365</a></p>`
        : `<p class="doux">Renseigner les réglages pour pouvoir se connecter.</p>`}
        <hr style="border:none;border-top:1px solid var(--ligne);margin:1rem 0">
        <h2>Quitter</h2>
        <p class="doux petit">Arrête l'application sur ce poste. Les données restent sur SharePoint.</p>
        <p><button id="quitter">Quitter Régie Partage</button></p>
      </section>
    </div>`;
  document.getElementById("f").onsubmit = async (e) => {
    e.preventDefault();
    const v = (id) => document.getElementById(id).value;
    const corps = { tenantId: v("tenant"), clientId: v("client"), siteUrl: v("site"), dossierRacine: v("racine"), nomSalle: v("salle") };
    if (await action(document.getElementById("ok"), () => api("POST", "/api/config", corps), "Réglages enregistrés")) {
      etat = await api("GET", "/api/etat");
      donnees = null;
      if (location.hash === "#/projets") afficher(); else location.hash = "#/projets";
    }
  };
  const deco = document.getElementById("deco");
  if (deco) deco.onclick = async () => { await action(deco, () => api("POST", "/api/deconnexion")); etat.compte = null; donnees = null; afficher(); };
  document.getElementById("quitter").onclick = async () => {
    await api("POST", "/api/quitter").catch(() => {});
    document.body.innerHTML = `<main><div class="carte bloc-connexion"><h1>Régie Partage est fermée</h1><p class="doux">Vous pouvez fermer cet onglet.</p></div></main>`;
  };
}

demarrer();
