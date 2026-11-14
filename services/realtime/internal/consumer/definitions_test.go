package consumer

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"testing"
	"time"
)

// The broker is set up from deploy/rabbitmq/definitions.json (rabbitmq-setup), not by this package,
// so these tests check that file: the queue the consumer reads must send what it rejects to a
// dead-letter queue, instead of RabbitMQ deleting it.

const deadLetterTTL = 14 * 24 * time.Hour

type definitions struct {
	Exchanges []struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		Durable bool   `json:"durable"`
	} `json:"exchanges"`
	Queues []struct {
		Name      string         `json:"name"`
		Durable   bool           `json:"durable"`
		Arguments map[string]any `json:"arguments"`
	} `json:"queues"`
	Bindings []struct {
		Source      string `json:"source"`
		Destination string `json:"destination"`
		RoutingKey  string `json:"routing_key"`
	} `json:"bindings"`
	Policies []struct {
		Name       string         `json:"name"`
		Pattern    string         `json:"pattern"`
		ApplyTo    string         `json:"apply-to"`
		Priority   int            `json:"priority"`
		Definition map[string]any `json:"definition"`
	} `json:"policies"`
}

func readDefinitions(t *testing.T, path string) (definitions, []byte) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var d definitions
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return d, raw
}

// policyOf returns the definition of the one policy that applies to queue. RabbitMQ applies only
// the highest-priority match, so two matches would make one of them silently do nothing.
func (d definitions) policyOf(t *testing.T, queue string) map[string]any {
	t.Helper()
	var found map[string]any
	n := 0
	for _, p := range d.Policies {
		if p.ApplyTo != "queues" && p.ApplyTo != "all" {
			continue
		}
		if regexp.MustCompile(p.Pattern).MatchString(queue) {
			found = p.Definition
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d policies apply to queue %q, want exactly 1", n, queue)
	}
	return found
}

func (d definitions) durableQueue(t *testing.T, name string) {
	t.Helper()
	for _, q := range d.Queues {
		if q.Name == name {
			if !q.Durable {
				t.Errorf("queue %q is not durable", name)
			}
			if len(q.Arguments) != 0 {
				// RabbitMQ refuses to change the arguments of an existing queue; settings go in policies.
				t.Errorf("queue %q has arguments %v; use a policy instead", name, q.Arguments)
			}
			return
		}
	}
	t.Fatalf("queue %q is not declared", name)
}

func TestTheConsumerQueueDeadLettersWhatItRejects(t *testing.T) {
	d, _ := readDefinitions(t, "../../../../deploy/rabbitmq/definitions.json")
	queue := Config{}.withDefaults().Queue
	d.durableQueue(t, queue)

	policy := d.policyOf(t, queue)
	dlx, _ := policy["dead-letter-exchange"].(string)
	key, _ := policy["dead-letter-routing-key"].(string)
	if dlx == "" || key != queue {
		t.Fatalf("policy of %q = %v, want a dead-letter-exchange and the queue's name as dead-letter-routing-key", queue, policy)
	}

	exchangeFound := false
	for _, e := range d.Exchanges {
		if e.Name == dlx {
			exchangeFound = true
			if e.Type != "direct" || !e.Durable {
				t.Errorf("exchange %q is %s, durable %v; want a durable direct exchange", dlx, e.Type, e.Durable)
			}
		}
	}
	if !exchangeFound {
		t.Fatalf("dead-letter exchange %q is not declared", dlx)
	}

	dead := queue + ".dead"
	d.durableQueue(t, dead)
	bound := false
	for _, b := range d.Bindings {
		if b.Source == dlx && b.Destination == dead && b.RoutingKey == key {
			bound = true
		}
	}
	if !bound {
		t.Fatalf("no binding %s -> %s with routing key %q", dlx, dead, key)
	}

	// The dead-letter queue keeps messages for a limited time and dead-letters nothing itself.
	deadPolicy := d.policyOf(t, dead)
	if ttl, _ := deadPolicy["message-ttl"].(float64); time.Duration(ttl)*time.Millisecond != deadLetterTTL {
		t.Errorf("message-ttl of %q = %v ms, want %s", dead, deadPolicy["message-ttl"], deadLetterTTL)
	}
	if _, ok := deadPolicy["dead-letter-exchange"]; ok {
		t.Errorf("%q has a dead-letter exchange: expired dead events must be dropped, not moved on", dead)
	}
}

// The Helm chart cannot read files outside its folder, so it keeps a copy that must stay the same.
func TestTheHelmChartHasTheSameDefinitions(t *testing.T) {
	_, compose := readDefinitions(t, "../../../../deploy/rabbitmq/definitions.json")
	_, helm := readDefinitions(t, "../../../../deploy/helm/clan/files/definitions.json")
	if !bytes.Equal(compose, helm) {
		t.Fatal("deploy/helm/clan/files/definitions.json differs from deploy/rabbitmq/definitions.json; copy it over")
	}
}
