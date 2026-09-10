// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package producer_test

// producer_test.go — производитель ConfigMap доказывается ИНЪЕКЦИЕЙ, а не
// прочтением (задача #1901).
//
// По каждой оси — подача с дефектом и законный близнец той же формы: без второго
// отрицание зеленело бы на всём сломанном. Подачи синтетические: инъекция,
// правящая рабочую копию, ломает соседние прогоны, а её собственный вердикт
// зависит от того, кто ещё в ней работает.
//
// Согласие производителя с ДЕРЕВОМ и с чартом (имя объекта одно на обоих,
// ключи — по одному на манифест дерева) — предмет соседнего каталога:
// deploy/iam_module_manifest_producer_test.go.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	manifestproducer "github.com/PRO-Robotech/corelib/modulemanifest/producer"
)

// syntheticTree — дерево из служб и профиля, собранное для одной подачи.
//
// Синтетика, а не правка дерева: инъекция, трогающая рабочую копию, ломает
// соседние прогоны, а её собственный вердикт зависит от того, кто ещё в ней
// работает.
func syntheticTree(t *testing.T, name string, manifests map[string]string) (root, profile string) {
	t.Helper()
	root = t.TempDir()
	for dir, body := range manifests {
		p := filepath.Join(root, "services", dir)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("каталог службы %s не заведён: %v", p, err)
		}
		if body == "" {
			continue // служба БЕЗ манифеста — законный близнец
		}
		if err := os.WriteFile(filepath.Join(p, "manifest.yaml"), []byte(body), 0o600); err != nil {
			t.Fatalf("манифест %s не записан: %v", dir, err)
		}
	}
	profile = filepath.Join(root, "values.yaml")
	body := "kaname:\n  manifests:\n    configMapName: " + name + "\n"
	if err := os.WriteFile(profile, []byte(body), 0o600); err != nil {
		t.Fatalf("профиль не записан: %v", err)
	}
	return root, profile
}

// TestManifestProducerDerivesTheNameInsteadOfCarryingACopy — имя ConfigMap
// ВЫВОДИТСЯ из профиля.
//
// Копия имени внутри производителя — это второе объявление одного предмета: под
// смонтировал бы объект чарта, производитель положил бы свой, каталог доставки
// приехал бы пустым, и снаружи это неотличимо от «модулей нет». Инъекция подаёт
// ДРУГОЕ имя: производитель обязан пойти за ним.
func TestManifestProducerDerivesTheNameInsteadOfCarryingACopy(t *testing.T) {
	const injected = "sobstvennoe-imya-stenda"
	root, profile := syntheticTree(t, injected, map[string]string{
		"vpc": "apiVersion: iam/v1\nmodule: vpc\n",
	})

	d, err := manifestproducer.Collect(root, []string{profile})
	if err != nil {
		t.Fatalf("производитель отверг синтетическую цепочку: %v (%s)", err, d.Census.Summary())
	}
	if d.Name != injected {
		t.Fatalf("производитель кладёт ConfigMap %q при объявленном %q — имя выписано "+
			"копией, а не выведено из профиля", d.Name, injected)
	}
}

// TestManifestProducerDerivesTheListFromTheTree — перечень выводится ОБХОДОМ, а
// служба без манифеста называется поимённо.
//
// Отрицание («служба без манифеста молча выпала») стоит в паре с положительным
// («служба с манифестом доехала»): без пары «перечень выведен» неотличимо от
// «перечень пуст».
func TestManifestProducerDerivesTheListFromTheTree(t *testing.T) {
	root, profile := syntheticTree(t, "kacho-module-manifests", map[string]string{
		"vpc": "apiVersion: iam/v1\nmodule: vpc\n",
		"iam": "apiVersion: iam/v1\nmodule: iam\n",
		"geo": "", // законный близнец: служба есть, манифеста нет
		"nlb": "apiVersion: iam/v1\nmodule: loadbalancer\n",
	})

	d, err := manifestproducer.Collect(root, []string{profile})
	if err != nil {
		t.Fatalf("производитель отверг синтетическое дерево: %v (%s)", err, d.Census.Summary())
	}
	if len(d.Sources) != 3 {
		t.Fatalf("собрано источников %d, ожидалось 3 (%s) — перечень выписан, а не выведен",
			len(d.Sources), d.Census.Summary())
	}
	if d.Census.ServiceDirs != 4 {
		t.Errorf("каталогов служб осмотрено %d, ожидалось 4 — «ноль находок» обязано быть "+
			"отличимо от «ноль прочитанного»", d.Census.ServiceDirs)
	}
	if strings.Join(d.Census.WithoutManifest, ",") != "geo" {
		t.Errorf("служба без манифеста не названа поимённо (%v) — доставка трёх манифестов "+
			"из четырёх выглядит снаружи ровно как доставка всех", d.Census.WithoutManifest)
	}
	for _, s := range d.Sources {
		if s.Key() != s.Dir+".manifest.yaml" {
			t.Errorf("ключ %q не является координатой источника %s — находку читателя "+
				"пришлось бы чинить перебором", s.Key(), s.Path)
		}
	}
}

// TestManifestProducerSeparatesNotDeclaredFromNothingToDeliver — «стенд доставку
// не объявляет» и «манифестов нет» разведены.
//
// Схлопни их — и подъём стенда либо ломается там, где всё верно (стенд просто не
// опирается на манифесты), либо продолжается там, где сломано (применён пустой
// ConfigMap, от которого служба откажется стартовать).
func TestManifestProducerSeparatesNotDeclaredFromNothingToDeliver(t *testing.T) {
	// (а) объявления нет — законный исход.
	rootA, profileA := syntheticTree(t, `""`, map[string]string{
		"vpc": "apiVersion: iam/v1\nmodule: vpc\n",
	})
	if _, err := manifestproducer.Collect(rootA, []string{profileA}); err == nil {
		t.Error("пустое объявление принято как имя — ConfigMap завелся бы без имени")
	} else if !strings.Contains(err.Error(), "не объявлена") {
		t.Errorf("«доставка не объявлена» пришла не своим исходом: %v", err)
	}

	// (б) объявление есть, манифестов нет — беспредметный обход.
	rootB, profileB := syntheticTree(t, "kacho-module-manifests", map[string]string{"geo": ""})
	_, err := manifestproducer.Collect(rootB, []string{profileB})
	if err == nil {
		t.Fatal("пустой ConfigMap собран — служба прочтёт его как сорванную доставку " +
			"и откажется стартовать")
	}
	if !strings.Contains(err.Error(), "нет ни одного") {
		t.Errorf("беспредметный обход пришёл не своим исходом: %v", err)
	}

	// (в) ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: объявление есть и манифест есть — исход годный.
	rootC, profileC := syntheticTree(t, "kacho-module-manifests", map[string]string{
		"vpc": "apiVersion: iam/v1\nmodule: vpc\n",
	})
	if _, err := manifestproducer.Collect(rootC, []string{profileC}); err != nil {
		t.Errorf("годная подача отвергнута — читатель отвергает всё: %v", err)
	}
}

// TestManifestProducerRefusesAnObjectLargerThanTheLimit — предел ConfigMap
// назван производителем, а не приходит отказом apiserver посреди подъёма стенда.
func TestManifestProducerRefusesAnObjectLargerThanTheLimit(t *testing.T) {
	big := "apiVersion: iam/v1\nmodule: vpc\n# " +
		strings.Repeat("x", manifestproducer.ConfigMapDataLimit) + "\n"
	root, profile := syntheticTree(t, "kacho-module-manifests", map[string]string{"vpc": big})

	d, err := manifestproducer.Collect(root, []string{profile})
	if err == nil {
		t.Fatalf("объект больше предела собран (%s) — отказ пришёл бы от apiserver "+
			"посреди подъёма стенда", d.Census.Summary())
	}
	if !strings.Contains(err.Error(), "предел") {
		t.Errorf("отказ не называет предела — читателю нечем решить, чинить файл или "+
			"поднимать предел: %v", err)
	}
}
