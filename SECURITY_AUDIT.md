# Rapport d'Audit de Sécurité — Navigatorr

> **Date :** Février 2026  
> **Portée :** Audit complet du code source du dépôt `Inobak/navigatorr`

---

## 1. Description du Projet

### Ce que fait Navigatorr

Navigatorr est un **serveur MCP** (Model Context Protocol) qui sert de pont entre des assistants IA (Claude Code, Cursor, etc.) et une stack media self-hosted composée de services *arr (Sonarr, Radarr, Lidarr, Readarr, Prowlarr, Bazarr, Overseerr) ainsi que le client torrent Transmission.

Concrètement, il permet à un assistant IA d'interagir en langage naturel avec des services de gestion media, sans que l'utilisateur n'ait à consulter manuellement des API ou des interfaces web.

### Pourquoi ce projet existe

Les services *arr exposent des API REST très riches mais peu documentées en pratique. Navigatorr automatise :

1. **La découverte d'API** — télécharge les specs OpenAPI officielles depuis GitHub et les indexe localement pour permettre une recherche full-text des endpoints disponibles.
2. **Les appels authentifiés** — gère les différentes méthodes d'authentification (header `X-Api-Key`, paramètre de requête, Basic Auth) pour tous les services configurés.
3. **La gestion Transmission** — expose une interface simplifiée vers le RPC Transmission (lister, ajouter, démarrer, arrêter, supprimer des torrents).
4. **Le transport MCP stdio** — communique avec le client IA via stdin/stdout en JSON-RPC, compatible avec n'importe quel hôte MCP.

### Architecture

```
Client MCP (Claude Code / Cursor)
        │  JSON-RPC via stdio
        ▼
   ┌─────────────────────────────┐
   │         Navigatorr          │
   │  ┌──────────────────────┐   │
   │  │  Outils MCP (tools/) │   │
   │  │  • call_api          │   │
   │  │  • list_services     │   │
   │  │  • search_api        │   │
   │  │  • transmission_*    │   │
   │  └──────────┬───────────┘   │
   │             │               │
   │  ┌──────────▼───────────┐   │
   │  │  Service Registry    │   │
   │  │  (arrservice/)       │   │
   │  └──────────┬───────────┘   │
   │             │               │
   │  ┌──────────▼───────────┐   │
   │  │  OpenAPI Store       │   │
   │  │  (openapi/)          │   │
   │  └──────────────────────┘   │
   └─────────────┬───────────────┘
                 │ HTTP
                 ▼
   Services *arr + Transmission
```

---

## 2. Résumé des Vulnérabilités

| # | Sévérité | Fichier(s) | Problème | Statut |
|---|----------|-----------|---------|--------|
| 1 | 🔴 Critique | `openapi/parser.go`, `openapi/fetcher.go` | `fmt.Printf` sur stdout corrompt le transport MCP JSON-RPC | ✅ Corrigé |
| 2 | 🔴 Critique | `openapi/parser.go` | `IsExternalRefsAllowed = true` permet une SSRF via les specs OpenAPI | ✅ Corrigé |
| 3 | 🟠 Haute | `arrservice/client.go`, `openapi/fetcher.go` | Absence de limite sur la taille des réponses HTTP (OOM/DoS) | ✅ Corrigé |
| 4 | 🟠 Haute | `transmission/client.go` | `defer resp.Body.Close()` dans une boucle entraîne une fuite de ressources | ✅ Corrigé |
| 5 | 🟡 Moyenne | `tools/api_call.go` | Méthode HTTP non validée, toute valeur arbitraire acceptée | ✅ Corrigé |
| 6 | 🟡 Moyenne | `openapi/cache.go` | Permissions des fichiers de cache trop permissives (0644) | ✅ Corrigé |
| 7 | 🟢 Faible | `config/config.go` | Pas de validation des URLs de services (schéma, hôte) | ⚠️ Documenté |
| 8 | 🟢 Faible | `arrservice/client.go` | L'URL de service est exposée dans les sorties MCP | ⚠️ Documenté |
| 9 | 🟢 Faible | `Dockerfile` | Image de base `alpine:latest` non épinglée | ⚠️ Documenté |

---

## 3. Détail des Vulnérabilités

### 🔴 #1 — Corruption du transport MCP via `fmt.Printf` sur stdout

**Fichiers :** `openapi/parser.go`, `openapi/fetcher.go`  
**Sévérité :** Critique  
**Statut :** ✅ Corrigé

**Description :**  
Le serveur MCP utilise le transport **stdio** (stdin/stdout) pour communiquer avec le client IA en JSON-RPC. Toute écriture parasite sur stdout provoque une corruption du protocole et peut faire planter la connexion MCP.

Deux appels `fmt.Printf(...)` écrivaient directement sur stdout au lieu d'utiliser le logger interne (qui écrit sur stderr) :

```go
// openapi/parser.go — AVANT
fmt.Printf("warning: spec validation for %s: %v\n", service, err)

// openapi/fetcher.go — AVANT
fmt.Printf("warning: failed to cache spec: %v\n", err)
```

**Correction :** Remplacement par des appels à `internal.Errorf(...)` qui écrivent sur stderr.

---

### 🔴 #2 — SSRF via références externes dans les specs OpenAPI

**Fichier :** `openapi/parser.go`  
**Sévérité :** Critique  
**Statut :** ✅ Corrigé

**Description :**  
La configuration du loader OpenAPI activait les références externes :

```go
// AVANT
loader.IsExternalRefsAllowed = true
```

Avec ce paramètre, une spec OpenAPI malveillante (ou compromise) pouvant être chargée via un `openapi_url` arbitraire dans la config, ou via l'endpoint `refresh_api_specs`, pourrait contenir des `$ref` pointant vers des URLs externes. Cela permettrait à un attaquant de :
- Forcer le serveur à contacter des hôtes internes non accessibles directement (SSRF)
- Exfiltrer des métadonnées d'environnement (par ex. endpoints AWS IMDS)
- Charger du contenu malveillant depuis des sources tierces

**Correction :** `loader.IsExternalRefsAllowed = false`

---

### 🟠 #3 — Absence de limite sur la taille des réponses HTTP (DoS / OOM)

**Fichiers :** `arrservice/client.go`, `openapi/fetcher.go`  
**Sévérité :** Haute  
**Statut :** ✅ Corrigé

**Description :**  
`io.ReadAll(resp.Body)` lit la totalité de la réponse en mémoire sans aucune limite. Un service compromis ou malveillant peut retourner une réponse de taille arbitraire, épuisant la mémoire du processus.

```go
// AVANT
respBody, err := io.ReadAll(resp.Body)
data, err := io.ReadAll(resp.Body)
```

**Correction :** Utilisation de `io.LimitReader` avec des limites adaptées :
- Réponses API *arr : **10 Mo** (`arrservice/client.go`)
- Specs OpenAPI : **50 Mo** (`openapi/fetcher.go`)

```go
// APRÈS
respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
data, err := io.ReadAll(io.LimitReader(resp.Body, maxSpecSize))
```

---

### 🟠 #4 — `defer` dans une boucle — fuite de ressources

**Fichier :** `transmission/client.go`  
**Sévérité :** Haute  
**Statut :** ✅ Corrigé

**Description :**  
Le `defer resp.Body.Close()` était placé à l'intérieur d'une boucle `for`. En Go, `defer` est scoped à la **fonction** et non à l'itération de boucle. Par conséquent, la connexion HTTP n'était fermée qu'au retour de la fonction `call()`, pas à la fin de chaque itération. Avec deux tentatives, cela signifie que des connexions TCP pouvaient rester ouvertes inutilement.

```go
// AVANT — defer dans la boucle
for attempt := 0; attempt < 2; attempt++ {
    ...
    defer resp.Body.Close()   // ⚠️ ne se ferme qu'au retour de call()
    ...
}
```

**Correction :** Remplacement par des fermetures explicites `resp.Body.Close()` à chaque point de sortie.

---

### 🟡 #5 — Méthode HTTP non validée dans `call_api`

**Fichier :** `tools/api_call.go`  
**Sévérité :** Moyenne  
**Statut :** ✅ Corrigé

**Description :**  
Le paramètre `method` du tool `call_api` était converti en majuscules mais jamais validé :

```go
method := strings.ToUpper(mcp.ParseString(req, "method", "GET"))
```

N'importe quelle valeur était acceptée (`CONNECT`, `TRACE`, `OPTIONS`, méthodes non standard, etc.), pouvant conduire à des comportements inattendus sur certains services ou proxies.

**Correction :** Validation contre la liste des méthodes HTTP autorisées : `GET`, `POST`, `PUT`, `PATCH`, `DELETE`.

---

### 🟡 #6 — Permissions de fichiers de cache trop permissives

**Fichier :** `openapi/cache.go`  
**Sévérité :** Moyenne  
**Statut :** ✅ Corrigé

**Description :**  
Les specs OpenAPI en cache étaient écrites avec des permissions `0644` (lecture par tout le monde) :

```go
// AVANT
return os.WriteFile(c.cacheFile(url), data, 0644)
```

Bien que les specs OpenAPI ne soient pas des données sensibles (ce sont des documents publics), restreindre les permissions au minimum nécessaire est une bonne pratique (principe du moindre privilège). Sur un système multi-utilisateurs, des fichiers lisibles par tous peuvent aussi être utilisés comme vecteur d'information sur les services configurés.

**Correction :** Permissions changées en `0600` (lecture/écriture uniquement par le propriétaire).

---

### 🟢 #7 — Absence de validation des URLs de services (Non corrigé)

**Fichier :** `config/config.go`  
**Sévérité :** Faible  
**Statut :** ⚠️ Documenté (non corrigé)

**Description :**  
Les URLs de services configurées dans `config.yaml` ne sont pas validées :
- Le schéma n'est pas vérifié (`http://` vs `https://` vs autre)
- Une URL vide ou malformée n'est détectée qu'à l'exécution de la première requête

Pour un contexte d'usage local et personnel, ce risque est très limité car l'utilisateur contrôle entièrement sa propre configuration.

**Recommandation :** Ajouter une validation basique (schéma `http` ou `https`, hôte non vide) dans la fonction `Load` du config.

---

### 🟢 #8 — URL des services exposée dans la sortie de `list_services`

**Fichier :** `tools/api_docs.go`  
**Sévérité :** Faible  
**Statut :** ⚠️ Documenté (comportement voulu)

**Description :**  
Le tool `list_services` expose les URLs des services configurés dans sa réponse JSON. Les clés API ne sont **pas** exposées, ce qui est correct. L'URL seule n'est pas une information sensible dans ce contexte, mais il est bon d'en être conscient.

---

### 🟢 #9 — Image Docker `alpine:latest` non épinglée

**Fichier :** `Dockerfile`  
**Sévérité :** Faible  
**Statut :** ⚠️ Documenté (non corrigé)

**Description :**

```dockerfile
FROM alpine:latest
```

L'utilisation d'un tag `latest` rend le build non reproductible et peut introduire des régressions ou des vulnérabilités lors de futures reconstructions si l'image de base est mise à jour.

**Recommandation :** Épingler à une version spécifique, par exemple `alpine:3.21`.

---

## 4. Bonnes Pratiques Déjà en Place

Le projet suit déjà plusieurs bonnes pratiques de sécurité :

- ✅ **TLS activé par défaut** — Les clients HTTP utilisent le transport Go standard avec vérification des certificats activée.
- ✅ **Gestion du CSRF Transmission** — Le token CSRF de Transmission est géré correctement selon la spécification RPC.
- ✅ **Timeouts HTTP** — Tous les clients HTTP ont un timeout de 30 secondes défini.
- ✅ **Contextes propagés** — Les requêtes HTTP utilisent `http.NewRequestWithContext` permettant l'annulation.
- ✅ **Logging sur stderr** — Le logger interne écrit sur stderr, séparant les logs du transport MCP.
- ✅ **Pas de secrets hardcodés** — Aucune clé API ni mot de passe codé en dur dans le code source.
- ✅ **Build multi-stage Docker** — Le Dockerfile utilise un build multi-stage pour réduire la surface d'attaque de l'image finale.
- ✅ **Fichier de config hors du dépôt** — Le `config.yaml` avec les secrets est dans `~/.config/navigatorr/` et non dans le dépôt.

---

## 5. Recommandations Complémentaires

1. **Chiffrement en transit** : Pour les services *arr distants (pas en localhost), utiliser HTTPS et considérer un proxy inversé (Nginx, Caddy) avec certificats valides.

2. **Rotation des clés API** : Les clés API des services *arr ne sont pas rotatives. Documenter une procédure de rotation.

3. **Validation des chemins API** : Le paramètre `path` de `call_api` n'est pas validé contre des séquences `../` ou d'autres tentatives de traversée de chemin. Dans ce contexte (URL HTTP standard), le risque est limité mais une validation basique serait bénéfique.

4. **Rate limiting** : Il n'y a pas de limitation du débit des appels API, ce qui pourrait permettre à un LLM mal configuré de saturer les services *arr.

5. **Audit log** : Pour un usage en production, considérer l'ajout d'un journal d'audit des appels API effectués (service, endpoint, méthode) sans les données sensibles.
