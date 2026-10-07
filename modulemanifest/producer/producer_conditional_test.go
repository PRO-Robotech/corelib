// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package producer_test

// producer_conditional_test.go — манифест модуля, доставляемый ТОЛЬКО цепочкам,
// где модуль включён (corelib#77).
//
// Предмет: обход `services/*/manifest.yaml` раздаёт найденное ВСЕМ цепочкам. У
// стендового модуля (проба-источник notify) манифест в боевой установке —
// посторонняя строка модели прав: применитель заводит по ней кортеж reader и
// запись выдачи на службу, которой в установке нет. Условие доставки обязано
// жить на стороне развёртывания и быть ТЕМ ЖЕ значением, которым включается
// сам модуль, — иначе манифест и нагрузка разойдутся молча.
//
// По каждой оси — подача с дефектом и законный близнец той же формы.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	manifestproducer "github.com/PRO-Robotech/corelib/modulemanifest/producer"
)

const (
	probeSource = "services/notify/probe/manifest.yaml"
	probeBody   = "apiVersion: iam/v1\nmodule: notify-probe\n"
	probeKey    = "notify-probe.manifest.yaml"
)

// condTree — дерево с одной безусловной службой (vpc) и стендовым манифестом
// пробы вне безусловного обхода. Профили пишутся вызывающим.
func condTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "services/vpc/manifest.yaml", "apiVersion: iam/v1\nmodule: vpc\n")
	write(t, root, probeSource, probeBody)
	return root
}

func write(t *testing.T, root, rel, body string) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("каталог %s не заведён: %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("%s не записан: %v", p, err)
	}
	return p
}

// declaring — профиль, объявляющий доставку и условный манифест пробы.
const declaring = "kaname:\n  manifests:\n    configMapName: kacho-module-manifests\n" +
	"    conditional:\n" +
	"      - source: " + probeSource + "\n" +
	"        enabledBy: notifyProbe.enabled\n"

func keys(d manifestproducer.Delivery) []string {
	var out []string
	for _, s := range d.Sources {
		out = append(out, s.Key())
	}
	return out
}

func hasKey(d manifestproducer.Delivery, k string) bool {
	for _, got := range keys(d) {
		if got == k {
			return true
		}
	}
	return false
}

// TestStandOnlyManifestIsWithheldFromAChainThatDoesNotEnableIt — боевой цепочке
// манифест пробы НЕ достаётся, а безусловный — достаётся.
func TestStandOnlyManifestIsWithheldFromAChainThatDoesNotEnableIt(t *testing.T) {
	root := condTree(t)
	p := write(t, root, "values.prod.yaml", declaring+"notifyProbe:\n  enabled: false\n")

	d, err := manifestproducer.Collect(root, []string{p})
	if err != nil {
		t.Fatalf("цепочка без пробы отвергнута: %v (%s)", err, d.Census.Summary())
	}
	if hasKey(d, probeKey) {
		t.Fatalf("манифест пробы доставлен цепочке, где проба выключена: ключи %v", keys(d))
	}
	if !hasKey(d, "vpc.manifest.yaml") {
		t.Fatalf("безусловный манифест потерян вместе с условным: ключи %v", keys(d))
	}
	if !strings.Contains(d.Census.Summary(), probeSource) {
		t.Errorf("удержанный манифест не назван переписью — «не доставлен по условию» "+
			"неотличим от «не найден»: %s", d.Census.Summary())
	}
}

// TestStandOnlyManifestIsDeliveredToAChainThatEnablesIt — законный близнец:
// включённой цепочке манифест достаётся, ключ — координата источника.
func TestStandOnlyManifestIsDeliveredToAChainThatEnablesIt(t *testing.T) {
	root := condTree(t)
	p := write(t, root, "values.dev.yaml", declaring+"notifyProbe:\n  enabled: true\n")

	d, err := manifestproducer.Collect(root, []string{p})
	if err != nil {
		t.Fatalf("включённая цепочка отвергнута: %v (%s)", err, d.Census.Summary())
	}
	if !hasKey(d, probeKey) || !hasKey(d, "vpc.manifest.yaml") {
		t.Fatalf("включённой цепочке доставлено %v, ожидались оба манифеста", keys(d))
	}
	for _, s := range d.Sources {
		if s.Key() == probeKey && (s.Path != probeSource || string(s.Body) != probeBody) {
			t.Errorf("условный источник доставлен не тем: путь %q, тело %q", s.Path, s.Body)
		}
	}
	out, err := manifestproducer.Render(d)
	if err != nil {
		t.Fatalf("объект не собран: %v", err)
	}
	if !strings.Contains(string(out), probeKey+":") {
		t.Errorf("ключ пробы не попал в объект ConfigMap:\n%s", out)
	}
}

// TestStandOnlyConditionFollowsTheProfileLayering — условие читается по
// наложению профилей, как его получает helm: верхний слой выключает пробу,
// включённую нижним, — манифест удерживается; и наоборот.
func TestStandOnlyConditionFollowsTheProfileLayering(t *testing.T) {
	root := condTree(t)
	base := write(t, root, "values.dev.yaml", declaring+"notifyProbe:\n  enabled: true\n  db: {}\n")
	off := write(t, root, "values.off.yaml", "notifyProbe:\n  enabled: false\n")
	on := write(t, root, "values.on.yaml", "notifyProbe:\n  enabled: true\n")

	d, err := manifestproducer.Collect(root, []string{base, off})
	if err != nil {
		t.Fatalf("цепочка отвергнута: %v", err)
	}
	if hasKey(d, probeKey) {
		t.Errorf("верхний слой выключил пробу, а манифест доставлен: %v", keys(d))
	}

	d, err = manifestproducer.Collect(root, []string{base, off, on})
	if err != nil {
		t.Fatalf("цепочка отвергнута: %v", err)
	}
	if !hasKey(d, probeKey) {
		t.Errorf("верхний слой включил пробу, а манифест удержан: %v", keys(d))
	}
}

// TestStandOnlyConditionAbsentWithholdsLikeTheChart — условие, которого цепочка
// не объявляет, читается так же, как его читает чарт (`(.Values.x).enabled`
// пусто — объекта нет): манифест удерживается и называется переписью.
func TestStandOnlyConditionAbsentWithholdsLikeTheChart(t *testing.T) {
	root := condTree(t)
	p := write(t, root, "values.yaml", declaring)

	d, err := manifestproducer.Collect(root, []string{p})
	if err != nil {
		t.Fatalf("цепочка отвергнута: %v", err)
	}
	if hasKey(d, probeKey) {
		t.Errorf("необъявленное условие прочитано как включение: %v", keys(d))
	}
	if !strings.Contains(d.Census.Summary(), "notifyProbe.enabled") {
		t.Errorf("перепись не называет условия, по которому манифест удержан: %s",
			d.Census.Summary())
	}
}

// TestStandOnlyDeclarationDefectsAreFindings — объявление, которое нельзя
// исполнить однозначно, есть находка, а не молчаливое удержание.
func TestStandOnlyDeclarationDefectsAreFindings(t *testing.T) {
	cases := []struct {
		name, profile, want string
	}{
		{
			name:    "условие не булево",
			profile: declaring + "notifyProbe:\n  enabled: \"yes\"\n",
			want:    "условие notifyProbe.enabled объявлено string",
		},
		{
			name: "источник уже доставляется безусловным обходом",
			profile: "kaname:\n  manifests:\n    configMapName: m\n    conditional:\n" +
				"      - source: services/vpc/manifest.yaml\n        enabledBy: notifyProbe.enabled\n",
			want: "services/vpc/manifest.yaml уже доставляется безусловным обходом",
		},
		{
			name: "источника нет в дереве — даже при выключенном условии",
			profile: "kaname:\n  manifests:\n    configMapName: m\n    conditional:\n" +
				"      - source: services/notify/gone/manifest.yaml\n        enabledBy: notifyProbe.enabled\n" +
				"notifyProbe:\n  enabled: false\n",
			want: "условный манифест services/notify/gone/manifest.yaml не прочитан",
		},
		{
			name: "источник вне каталога служб",
			profile: "kaname:\n  manifests:\n    configMapName: m\n    conditional:\n" +
				"      - source: ../outside/manifest.yaml\n        enabledBy: notifyProbe.enabled\n",
			want: "источник ../outside/manifest.yaml не путь под services/",
		},
		{
			name: "источник не манифест",
			profile: "kaname:\n  manifests:\n    configMapName: m\n    conditional:\n" +
				"      - source: services/notify/probe/values.yaml\n        enabledBy: notifyProbe.enabled\n",
			want: "источник services/notify/probe/values.yaml не манифест",
		},
		{
			name: "условие не названо",
			profile: "kaname:\n  manifests:\n    configMapName: m\n    conditional:\n" +
				"      - source: " + probeSource + "\n",
			want: "enabledBy не объявлен",
		},
		{
			name: "один источник объявлен дважды",
			profile: "kaname:\n  manifests:\n    configMapName: m\n    conditional:\n" +
				"      - source: " + probeSource + "\n        enabledBy: a.enabled\n" +
				"      - source: " + probeSource + "\n        enabledBy: b.enabled\n",
			want: "уже занятый " + probeSource,
		},
		{
			name: "перечень не список",
			profile: "kaname:\n  manifests:\n    configMapName: m\n    conditional: " +
				probeSource + "\n",
			want: "kaname.manifests.conditional: ожидался перечень",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := condTree(t)
			write(t, root, "services/notify/probe/values.yaml", "x: 1\n")
			p := write(t, root, "values.yaml", c.profile)
			d, err := manifestproducer.Collect(root, []string{p})
			if err == nil {
				t.Fatalf("дефектное объявление принято: ключи %v (%s)", keys(d), d.Census.Summary())
			}
			if errors.Is(err, manifestproducer.ErrNotDeclared) || errors.Is(err, manifestproducer.ErrNoManifests) {
				t.Fatalf("дефект объявления пришёл законным исходом: %v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("находка не называет координаты %q: %v", c.want, err)
			}
		})
	}
}

// TestStandOnlyManifestAloneIsEnoughToDeliver — включённый условный манифест
// при пустом безусловном обходе — не беспредметный обход; выключенный — он.
func TestStandOnlyManifestAloneIsEnoughToDeliver(t *testing.T) {
	root := t.TempDir()
	write(t, root, probeSource, probeBody)
	on := write(t, root, "on.yaml", declaring+"notifyProbe:\n  enabled: true\n")
	off := write(t, root, "off.yaml", declaring+"notifyProbe:\n  enabled: false\n")

	d, err := manifestproducer.Collect(root, []string{on})
	if err != nil || !hasKey(d, probeKey) {
		t.Fatalf("единственный включённый манифест не доставлен: %v, ключи %v", err, keys(d))
	}
	if _, err := manifestproducer.Collect(root, []string{off}); !errors.Is(err, manifestproducer.ErrNoManifests) {
		t.Errorf("удержан единственный манифест, а исход не «нечего доставить»: %v", err)
	}
}
