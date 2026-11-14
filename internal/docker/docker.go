// Package docker talks to the Docker API through its socket, with no dependency: listing containers, inspecting them,
// connecting them to a network, and following the event stream.
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

// Client is a Docker API client.
type Client struct {
	http *http.Client
}

// New returns a client on Docker's Unix socket.
func New(socket string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}
	return &Client{http: &http.Client{Transport: transport}}
}

// Summary is a container as the list returns it.
type Summary struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Labels  map[string]string `json:"Labels"`
	Created int64             `json:"Created"`
}

// Container is a container as its inspection returns it.
type Container struct {
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

// ShortName is the container name, without its leading slash.
func (c *Container) ShortName() string { return strings.TrimPrefix(c.Name, "/") }

// ExposedTCPPorts are the TCP ports the image exposes.
func (c *Container) ExposedTCPPorts() []int {
	var ports []int
	for p := range c.Config.ExposedPorts {
		number, proto, _ := strings.Cut(p, "/")
		if proto != "" && proto != "tcp" {
			continue
		}
		if n, err := strconv.Atoi(number); err == nil {
			ports = append(ports, n)
		}
	}
	return ports
}

// Event is a Docker event.
type Event struct {
	Type   string `json:"Type"`
	Action string `json:"Action"`
	Actor  struct {
		ID         string            `json:"ID"`
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
	TimeNano int64 `json:"timeNano"`
}

// Containers lists the running containers that carry this label.
func (c *Client) Containers(ctx context.Context, label string) ([]Summary, error) {
	filters, _ := json.Marshal(map[string][]string{"label": {label}, "status": {"running"}})
	var list []Summary
	err := c.do(ctx, http.MethodGet, "/containers/json?filters="+url.QueryEscape(string(filters)), nil, &list)
	return list, err
}

// Inspect returns a container's details.
func (c *Client) Inspect(ctx context.Context, id string) (*Container, error) {
	var ct Container
	if err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, &ct); err != nil {
		return nil, err
	}
	return &ct, nil
}

// Connect connects a container to a network; when it already is, nothing changes.
func (c *Client) Connect(ctx context.Context, network, id string) error {
	body, _ := json.Marshal(map[string]string{"Container": id})
	err := c.do(ctx, http.MethodPost, "/networks/"+url.PathEscape(network)+"/connect", body, nil)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return err
}

// Events follows the events of the containers that carry this label, since `since`, until the context or the stream
// ends.
func (c *Client) Events(ctx context.Context, since time.Time, label string, actions []string, receive func(Event)) error {
	filters, _ := json.Marshal(map[string][]string{"type": {"container"}, "event": actions, "label": {label}})
	path := fmt.Sprintf("/events?since=%d&filters=%s", since.Unix(), url.QueryEscape(string(filters)))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("docker %s: %s %s", path, resp.Status, bytes.TrimSpace(body))
	}
	dec := json.NewDecoder(bufio.NewReader(resp.Body))
	for {
		var e Event
		if err := dec.Decode(&e); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		receive(e)
	}
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, into any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("docker %s %s: %s %s", method, path, resp.Status, bytes.TrimSpace(content))
	}
	if into != nil && len(content) > 0 {
		return json.Unmarshal(content, into)
	}
	return nil
}
