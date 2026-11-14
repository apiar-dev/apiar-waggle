// Package docker parle à l'API de Docker par son socket, sans dépendance : la liste des conteneurs, leur détail, le
// branchement sur un réseau, et le flux des événements.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client : un client de l'API de Docker.
type Client struct {
	http *http.Client
}

// Nouveau : un client sur le socket Unix de Docker.
func Nouveau(socket string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}
	return &Client{http: &http.Client{Transport: transport}}
}

// Resume : un conteneur, tel que la liste le donne.
type Resume struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Labels  map[string]string `json:"Labels"`
	Created int64             `json:"Created"`
}

// Detail : un conteneur, tel que son inspection le donne.
type Detail struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		Labels       map[string]string   `json:"Labels"`
		ExposedPorts map[string]struct{} `json:"ExposedPorts"`
	} `json:"Config"`
	State struct {
		Running bool `json:"Running"`
	} `json:"State"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

// Nom : le nom du conteneur, sans sa barre.
func (d *Detail) Nom() string { return strings.TrimPrefix(d.Name, "/") }

// PortsExposes : les ports TCP que l'image expose.
func (d *Detail) PortsExposes() []int {
	var ports []int
	for p := range d.Config.ExposedPorts {
		numero, proto, _ := strings.Cut(p, "/")
		if proto != "" && proto != "tcp" {
			continue
		}
		if n, err := strconv.Atoi(numero); err == nil {
			ports = append(ports, n)
		}
	}
	return ports
}

// Evenement : un événement de Docker.
type Evenement struct {
	Type   string `json:"Type"`
	Action string `json:"Action"`
	Actor  struct {
		ID         string            `json:"ID"`
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
	TimeNano int64 `json:"timeNano"`
}

// Conteneurs : les conteneurs en marche qui portent cette étiquette.
func (c *Client) Conteneurs(ctx context.Context, etiquette string) ([]Resume, error) {
	filtres, _ := json.Marshal(map[string][]string{"label": {etiquette}, "status": {"running"}})
	var liste []Resume
	err := c.lire(ctx, http.MethodGet, "/containers/json?filters="+url.QueryEscape(string(filtres)), nil, &liste)
	return liste, err
}

// Inspecter : le détail d'un conteneur.
func (c *Client) Inspecter(ctx context.Context, id string) (*Detail, error) {
	var d Detail
	if err := c.lire(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Brancher branche un conteneur sur un réseau ; déjà branché, rien ne change.
func (c *Client) Brancher(ctx context.Context, reseau, id string) error {
	corps, _ := json.Marshal(map[string]string{"Container": id})
	err := c.lire(ctx, http.MethodPost, "/networks/"+url.PathEscape(reseau)+"/connect", corps, nil)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return err
}

// Evenements suit les événements des conteneurs qui portent cette étiquette, depuis `depuis`, jusqu'à la fin du contexte
// ou du flux.
func (c *Client) Evenements(ctx context.Context, depuis time.Time, etiquette string, actions []string, recevoir func(Evenement)) error {
	filtres, _ := json.Marshal(map[string][]string{"type": {"container"}, "event": actions, "label": {etiquette}})
	chemin := fmt.Sprintf("/events?since=%d&filters=%s", depuis.Unix(), url.QueryEscape(string(filtres)))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+chemin, nil)
	if err != nil {
		return err
	}
	rep, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer rep.Body.Close()
	if rep.StatusCode != http.StatusOK {
		corps, _ := io.ReadAll(rep.Body)
		return fmt.Errorf("docker %s : %s %s", chemin, rep.Status, bytes.TrimSpace(corps))
	}
	dec := json.NewDecoder(bufio.NewReader(rep.Body))
	for {
		var e Evenement
		if err := dec.Decode(&e); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		recevoir(e)
	}
}

func (c *Client) lire(ctx context.Context, methode, chemin string, corps []byte, dans any) error {
	var lecteur io.Reader
	if corps != nil {
		lecteur = bytes.NewReader(corps)
	}
	req, err := http.NewRequestWithContext(ctx, methode, "http://docker"+chemin, lecteur)
	if err != nil {
		return err
	}
	if corps != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rep, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer rep.Body.Close()
	contenu, err := io.ReadAll(rep.Body)
	if err != nil {
		return err
	}
	if rep.StatusCode >= 300 {
		return fmt.Errorf("docker %s %s : %s %s", methode, chemin, rep.Status, bytes.TrimSpace(contenu))
	}
	if dans != nil && len(contenu) > 0 {
		return json.Unmarshal(contenu, dans)
	}
	return nil
}
