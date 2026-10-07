// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package producer_test

// producer_layering_test.go — значения цепочки профилей накладываются ПРАВИЛОМ
// HELM, а не «последним найденным путём» (возврат ревью corelib#100).
//
// Предмет: условие доставки и имя ConfigMap обязаны читаться тем же наложением,
// которым их получает чарт. У helm не-отображение на ЛЮБОМ префиксе пути — null
// в том числе — перекрывает прежнее значение целиком: `notifyProbe: null` после
// `notifyProbe: {enabled: true}` удаляет ветку, и модуль не рендерится. Поиск
// пути по каждому профилю отдельно такого слоя «не видит» (пути в нём нет) и
// берёт прежнее `true` — манифест доставляется установке, где модуля нет.
//
// Замер helm v4.2.4 на минимальном чарте с условием `(.Values.notifyProbe).enabled`
// (тем же, что у чарта пробы):
//
//	-f a                      → объект 1 раз
//	-f a -f {notifyProbe: null}         → 0 раз
//	-f a -f {notifyProbe: {db: {}}}     → 1 раз (отображение без ключа хранит прежнее)
//	-f {notifyProbe: null} -f a         → 1 раз
//	-f a -f {notifyProbe: false}        → отказ рендера «can't evaluate field enabled in type bool»
//	-f a -f {kaname: null}              → имя ConfigMap пусто
//
// По каждой оси — подача с дефектом и законный близнец той же формы.

import (
	"errors"
	"strings"
	"testing"

	manifestproducer "github.com/PRO-Robotech/corelib/modulemanifest/producer"
)

// TestStandOnlyConditionParentResetByALaterProfileWithholds — null на РОДИТЕЛЕ
// условия после включения удаляет ветку, как у helm: манифест удерживается и
// назван переписью.
func TestStandOnlyConditionParentResetByALaterProfileWithholds(t *testing.T) {
	cases := []struct{ name, later string }{
		{"родитель null", "notifyProbe: null\n"},
		{"лист null", "notifyProbe:\n  enabled: null\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := condTree(t)
			on := write(t, root, "values.on.yaml", declaring+"notifyProbe:\n  enabled: true\n")
			reset := write(t, root, "values.reset.yaml", c.later)

			d, err := manifestproducer.Collect(root, []string{on, reset})
			if err != nil {
				t.Fatalf("цепочка отвергнута: %v (%s)", err, d.Census.Summary())
			}
			if hasKey(d, probeKey) {
				t.Fatalf("верхний слой снял ветку условия, helm модуль не рендерит, а манифест "+
					"доставлен: ключи %v (%s)", keys(d), d.Census.Summary())
			}
			if !hasKey(d, "vpc.manifest.yaml") {
				t.Errorf("безусловный манифест потерян: ключи %v", keys(d))
			}
			if !strings.Contains(d.Census.Summary(), "notifyProbe.enabled=не объявлено") {
				t.Errorf("удержанный манифест не назван переписью со значением условия: %s",
					d.Census.Summary())
			}
		})
	}
}

// TestStandOnlyConditionParentMapKeepsTheEarlierValue — законные близнецы:
// отображение без ключа условия прежнее значение ХРАНИТ (helm сливает
// отображения), а null под включением снизу не мешает включению сверху.
func TestStandOnlyConditionParentMapKeepsTheEarlierValue(t *testing.T) {
	cases := []struct {
		name  string
		chain []string
	}{
		{"отображение без ключа поверх включения", []string{
			declaring + "notifyProbe:\n  enabled: true\n",
			"notifyProbe:\n  db: {}\n",
		}},
		{"включение поверх null", []string{
			declaring + "notifyProbe: null\n",
			"notifyProbe:\n  enabled: true\n",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := condTree(t)
			var chain []string
			for i, body := range c.chain {
				chain = append(chain, write(t, root, "values."+string(rune('a'+i))+".yaml", body))
			}
			d, err := manifestproducer.Collect(root, chain)
			if err != nil {
				t.Fatalf("цепочка отвергнута: %v (%s)", err, d.Census.Summary())
			}
			if !hasKey(d, probeKey) {
				t.Fatalf("helm модуль рендерит, а манифест удержан: ключи %v (%s)",
					keys(d), d.Census.Summary())
			}
		})
	}
}

// TestStandOnlyConditionScalarParentIsAFinding — скаляр на родителе условия
// после включения: helm такую цепочку не рендерит вовсе (отказ шаблона), и
// производитель обязан не доставить манифест, а назвать координату — не
// вернуться молча к прежнему `true`.
func TestStandOnlyConditionScalarParentIsAFinding(t *testing.T) {
	root := condTree(t)
	on := write(t, root, "values.on.yaml", declaring+"notifyProbe:\n  enabled: true\n")
	scalar := write(t, root, "values.scalar.yaml", "notifyProbe: false\n")

	d, err := manifestproducer.Collect(root, []string{on, scalar})
	if err == nil {
		t.Fatalf("скаляр на родителе условия принят: ключи %v (%s)", keys(d), d.Census.Summary())
	}
	if errors.Is(err, manifestproducer.ErrNotDeclared) || errors.Is(err, manifestproducer.ErrNoManifests) {
		t.Fatalf("дефект наложения пришёл законным исходом: %v", err)
	}
	if !strings.Contains(err.Error(), "notifyProbe объявлено bool") {
		t.Errorf("находка не называет координаты скаляра: %v", err)
	}
	if hasKey(d, probeKey) {
		t.Errorf("при находке манифест пробы всё же собран: %v", keys(d))
	}
}

// TestDeliveryDeclarationResetByALaterProfileIsNotDeclared — тот же закон для
// имени ConfigMap и перечня условных: null на префиксе пути снимает объявление
// доставки (helm отдаёт чарту пустое имя), отображение без ключа его хранит.
func TestDeliveryDeclarationResetByALaterProfileIsNotDeclared(t *testing.T) {
	cases := []struct {
		name, later string
		declared    bool
	}{
		{"kaname: null", "kaname: null\n", false},
		{"manifests: null", "kaname:\n  manifests: null\n", false},
		{"близнец: kaname без ключа manifests", "kaname:\n  other: {}\n", true},
		{"близнец: manifests без ключа имени", "kaname:\n  manifests:\n    other: x\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := condTree(t)
			on := write(t, root, "values.on.yaml", declaring+"notifyProbe:\n  enabled: true\n")
			later := write(t, root, "values.later.yaml", c.later)

			d, err := manifestproducer.Collect(root, []string{on, later})
			if !c.declared {
				if !errors.Is(err, manifestproducer.ErrNotDeclared) {
					t.Fatalf("верхний слой снял объявление доставки, а исход %v, имя %q, ключи %v",
						err, d.Name, keys(d))
				}
				return
			}
			if err != nil {
				t.Fatalf("цепочка отвергнута: %v (%s)", err, d.Census.Summary())
			}
			if d.Name != "kacho-module-manifests" || !hasKey(d, probeKey) {
				t.Fatalf("отображение без ключа потеряло прежнее объявление: имя %q, ключи %v",
					d.Name, keys(d))
			}
		})
	}
}

// TestDeliveryNameScalarIsAFinding — имя ConfigMap, объявленное не строкой, —
// находка с координатой, а не молчаливый возврат к прежнему слою.
func TestDeliveryNameScalarIsAFinding(t *testing.T) {
	root := condTree(t)
	on := write(t, root, "values.on.yaml", declaring)
	later := write(t, root, "values.later.yaml", "kaname:\n  manifests:\n    configMapName: 42\n")

	_, err := manifestproducer.Collect(root, []string{on, later})
	if err == nil || errors.Is(err, manifestproducer.ErrNotDeclared) {
		t.Fatalf("имя ConfigMap не строкой принято: %v", err)
	}
	if !strings.Contains(err.Error(), "kaname.manifests.configMapName объявлено int") {
		t.Errorf("находка не называет координаты: %v", err)
	}
}
