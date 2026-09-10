// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcclient

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/backoff"
)

// dial_test.go — соединение с соседом собирается ТЕМИ ЖЕ параметрами, какими
// его собирал снятый сторонний строитель.
//
// Пробы утверждают ЧИСЛА и СТРОКИ, а не «функция позвана»: предмет здесь —
// паритет параметров, и он проверяется только их значениями.

func TestPeerTargetKeepsResolutionOfTheRetiredBuilder(t *testing.T) {
	cases := []struct {
		name       string
		endpoint   string
		roundRobin bool
		want       string
	}{
		// Голый host:port резолвился поведением `DialContext` — схема
		// `passthrough`: имя отдаётся сетевому набирателю как есть. Схема
		// названа ЯВНО, потому что умолчание `grpc.NewClient` другое (`dns`).
		{"голый адрес", "kaname.kacho.svc:9090", false, "passthrough:///kaname.kacho.svc:9090"},
		// Схему `tcp://` снятый строитель срезал сам. Оператор, задавший её
		// сегодня, обязан продолжать работать.
		{"схема tcp срезается", "tcp://kacho-geo.kacho.svc:9090", false, "passthrough:///kacho-geo.kacho.svc:9090"},
		// Адрес, сам назвавший резолвер, не трогается.
		{"unix проходит как есть", "unix:///var/run/kacho.sock", false, "unix:///var/run/kacho.sock"},
		{"dns проходит как есть", "dns:///kaname.kacho.svc:9090", false, "dns:///kaname.kacho.svc:9090"},
		// Распределение по адресам требует резолвера, отдающего ВСЕ адреса;
		// `passthrough` отдаёт один, поэтому здесь схема другая.
		{"распределение требует dns", "kaname.kacho.svc:9090", true, "dns:///kaname.kacho.svc:9090"},
		{"пробелы срезаются", "  kaname.kacho.svc:9090  ", false, "passthrough:///kaname.kacho.svc:9090"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PeerTarget(c.endpoint, c.roundRobin); got != c.want {
				t.Fatalf("PeerTarget(%q, %v) = %q, ожидалось %q", c.endpoint, c.roundRobin, got, c.want)
			}
		})
	}
}

func TestPeerConnectParamsMirrorTheRetiredBackoff(t *testing.T) {
	const d = 10 * time.Second
	got := PeerConnectParams(d)
	want := backoff.DefaultConfig
	want.BaseDelay = d / 10
	want.Multiplier = 1.01
	want.Jitter = 0.1
	want.MaxDelay = d
	if got.Backoff != want {
		t.Fatalf("backoff = %+v, ожидался %+v", got.Backoff, want)
	}
	if got.MinConnectTimeout != d/10 {
		t.Fatalf("MinConnectTimeout = %v, ожидался %v", got.MinConnectTimeout, d/10)
	}
}

// Срок меньше секунды поднимался до секунды — иначе задержка вырождалась в
// сотые доли и повтор соединения превращался в занятое ожидание.
func TestPeerConnectParamsRaisesASubSecondDeadline(t *testing.T) {
	got := PeerConnectParams(200 * time.Millisecond)
	if got.Backoff.MaxDelay != time.Second {
		t.Fatalf("MaxDelay = %v, ожидалась 1s", got.Backoff.MaxDelay)
	}
	if got.MinConnectTimeout != 100*time.Millisecond {
		t.Fatalf("MinConnectTimeout = %v, ожидалось 100ms", got.MinConnectTimeout)
	}
}

func TestPeerServiceConfigRetriesOnUnavailableOnly(t *testing.T) {
	raw := PeerServiceConfigJSON(3, false)
	if raw == "" {
		t.Fatal("при retries=3 конфигурация пуста — повтора не будет")
	}
	var cfg struct {
		LoadBalancingConfig []map[string]any `json:"loadBalancingConfig"`
		MethodConfig        []struct {
			RetryPolicy struct {
				MaxAttempts          int      `json:"maxAttempts"`
				RetryableStatusCodes []string `json:"retryableStatusCodes"`
			} `json:"retryPolicy"`
		} `json:"methodConfig"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("конфигурация не разбирается как JSON: %v\n%s", err, raw)
	}
	if len(cfg.MethodConfig) != 1 {
		t.Fatalf("methodConfig: %d записей", len(cfg.MethodConfig))
	}
	// maxAttempts считает ИСХОДНУЮ попытку, поэтому retries+1.
	if got := cfg.MethodConfig[0].RetryPolicy.MaxAttempts; got != 4 {
		t.Fatalf("maxAttempts = %d, ожидалось 4 (retries+1)", got)
	}
	codes := cfg.MethodConfig[0].RetryPolicy.RetryableStatusCodes
	if len(codes) != 1 || codes[0] != "UNAVAILABLE" {
		t.Fatalf("повторяемые коды = %v, ожидался ровно UNAVAILABLE", codes)
	}
	if len(cfg.LoadBalancingConfig) != 0 {
		t.Fatalf("без распределения балансировщик объявлен: %v", cfg.LoadBalancingConfig)
	}
}

// Законный близнец предыдущей: retries=0 означало у снятого строителя
// ОТСУТСТВИЕ повтора, а не «повтор без попыток».
func TestPeerServiceConfigIsEmptyWithoutRetriesAndWithoutBalancing(t *testing.T) {
	if got := PeerServiceConfigJSON(0, false); got != "" {
		t.Fatalf("при retries=0 и без распределения конфигурация непуста: %s", got)
	}
}

func TestPeerServiceConfigDeclaresRoundRobinWhenAsked(t *testing.T) {
	raw := PeerServiceConfigJSON(0, true)
	if !strings.Contains(raw, "round_robin") {
		t.Fatalf("распределение не объявлено: %s", raw)
	}
	if strings.Contains(raw, "retryPolicy") {
		t.Fatalf("при retries=0 объявлена политика повтора: %s", raw)
	}
}

// Соединение действительно собирается: `grpc.NewClient` не набирает сразу,
// поэтому проба утверждает разбор адреса и применимость параметров, а не
// доступность соседа.
func TestDialPeerBuildsAConnectionWithoutReachingThePeer(t *testing.T) {
	cc, err := DialPeer(PeerDialOptions{
		Endpoint:      "kaname.kacho.svc:9090",
		Retries:       3,
		DialTimeout:   10 * time.Second,
		KeepAliveTime: 30 * time.Second,
		UserAgent:     "kacho-test",
	})
	if err != nil {
		t.Fatalf("DialPeer: %v", err)
	}
	defer func() { _ = cc.Close() }()
	// Target() отдаёт адрес ВМЕСТЕ со схемой — она и есть предмет утверждения:
	// молчаливый переход на умолчание `grpc.NewClient` сменил бы её на `dns`.
	if got := cc.Target(); got != "passthrough:///kaname.kacho.svc:9090" {
		t.Fatalf("Target() = %q", got)
	}
}

func TestDialPeerRejectsAnEmptyEndpoint(t *testing.T) {
	if _, err := DialPeer(PeerDialOptions{Endpoint: "   "}); err == nil {
		t.Fatal("пустой адрес принят — отказ обязан быть синхронным")
	}
}
