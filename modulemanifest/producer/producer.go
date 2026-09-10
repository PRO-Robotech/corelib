// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package modulemanifests — ПРОИЗВОДИТЕЛЬ ConfigMap с манифестами модулей
// (задача #1901).
//
// # Предмет
//
// Доставка манифестов заведена целиком (#1875): чарт монтирует именованный
// ConfigMap, процесс читает смонтированный каталог, страж старта отказывает на
// сорванной доставке. Наполнить этот ConfigMap в дереве было НЕЧЕМ — ни цели
// сборки, ни шага подъёма стенда, ни объявления внешнего конвейера. Пока
// производителя нет, опору на манифесты нельзя объявить ни в одном профиле: под
// поднимется с пустым каталогом, а процесс откажет, назвав сорванную доставку.
//
// Здесь эта половина и заводится: перечень манифестов ВЫВОДИТСЯ из дерева, имя
// ConfigMap ВЫВОДИТСЯ из тех же профилей, которые читает helm, а на выходе —
// объект, который применяет тот, кто поднимает стенд.
//
// # Почему перечень выводится, а не выписывается
//
// Выписанный перечень модулей разошёлся бы с деревом молча — и разошёлся бы
// именно тогда, когда заводят новый модуль: манифест в дереве есть, до службы он
// не доезжает, и ни одна проверка об этом не говорит. Поэтому источники
// собираются обходом `services/*/manifest.yaml`, а служба БЕЗ манифеста
// называется переписью поимённо: «ноль находок» обязано быть отличимо от «ноль
// прочитанного».
//
// # Почему ключ ConfigMap — КАТАЛОГ СЛУЖБЫ, а не имя модуля
//
// Ключ ConfigMap становится именем файла в каталоге доставки, и он же стоит
// координатой в находке читателя. Каталог службы есть ПРЯМАЯ координата
// источника (`services/<ключ>/manifest.yaml`) — по ней находку чинят, не держа в
// голове ничего лишнего. Имя модуля такой координатой не является: каталог `nlb`
// объявляет модуль `loadbalancer`, и по имени модуля источник пришлось бы искать.
//
// Форма ключа при этом ВЫНУЖДЕННАЯ, а не выбранная: имя `manifest.yaml` в одном
// ConfigMap может быть только одно, а подкаталога внутри ключа не бывает вовсе —
// замер обеими командами приведён в
// `services/iam/internal/manifest/delivery.go`.
package producer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PRO-Robotech/corelib/modulemanifest"
	"gopkg.in/yaml.v3"
)

// servicesDir — каталог, под которым лежат службы платформы. Одно место: второе
// разошлось бы с первым молча, и часть модулей перестала бы находиться, не дав
// ни одной находки.
const servicesDir = "services"

// manifestFileName — имя манифеста В ДЕРЕВЕ. В каталоге доставки имя другое, и
// это разные предметы, а не одно имя в двух местах; собирает ключ доставки
// `modulemanifest.DeliveryKey` (см. Source.Key).
//
// Значение берётся у ЕДИНСТВЕННОГО объявления (`pkg/modulemanifest`), а не
// пишется здесь. Читателей у имени трое, и правило `internal` запрещает этому
// производителю импортировать загрузчик iam, — поэтому «свести к одному
// объявлению у iam» неисполнимо by construction, и до заведения пакета имя
// стояло тремя копиями. Расходятся копии МОЛЧА: производитель, разошедшийся с
// обходом, кладёт в ConfigMap не то множество, и снаружи это неотличимо от
// «модулей нет» (задача #1934).
const manifestFileName = modulemanifest.FileName

// ConfigMapDataLimit — сколько байт манифестов помещается в один ConfigMap.
//
// Величина не наша: apiserver отвергает объект больше мебибайта целиком. Замер
// дерева на день заведения — 95 524 байта в шести манифестах, то есть запас
// одиннадцатикратный. Предел назван здесь, чтобы превышение приходило переписью
// производителя, а не отказом apiserver посреди подъёма стенда: второе
// диагностируется дороже при том же исходе.
const ConfigMapDataLimit = 1 << 20

// chartValuesPath — путь к объявлению имени ConfigMap в профиле умбреллы.
//
// Читается ИМЕННО он, а не своя копия имени: имя ConfigMap обязано быть ОДНИМ
// объявлением на производителя и на чарт, иначе они разойдутся молча — под
// смонтирует один объект, производитель положит другой, каталог доставки
// приедет пустым, и снаружи это неотличимо от «модулей нет».
var chartValuesPath = []string{"kaname", "manifests", "configMapName"}

// ErrNotDeclared — цепочка профилей доставку не объявляет.
//
// Это ЗАКОННЫЙ исход, а не отказ: стенд вправе не опираться на манифесты, и
// тогда ConfigMap заводить нечего. Отдельная ошибка, а не пустое имя, потому что
// вызывающему предстоит развести эти исходы по разным кодам возврата.
var ErrNotDeclared = errors.New("доставка манифестов цепочкой профилей не объявлена")

// ErrNoManifests — доставка объявлена, а манифеста в дереве нет ни одного.
//
// Беспредметный обход: положить в ConfigMap нечего, и объявить его пустым
// нельзя — пустой каталог доставки процесс читает как сорванную доставку и
// отказывается стартовать.
var ErrNoManifests = errors.New("манифеста в дереве нет ни одного")

// Source — манифест модуля, найденный в дереве.
type Source struct {
	// Dir — каталог службы под `services/`. Он же — начало ключа ConfigMap.
	Dir string
	// Path — путь манифеста ОТ КОРНЯ ДЕРЕВА: координата для находки.
	Path string
	// Body — содержимое, доставляемое побайтово. Производитель манифест НЕ
	// разбирает: годность документа судит читатель на старте службы и гейт
	// дерева, и второй судья разошёлся бы с ними формой.
	Body []byte
}

// Key — ключ ConfigMap для этого источника.
func (s Source) Key() string { return modulemanifest.DeliveryKey(s.Dir) }

// Census — объём осмотренного. Печатается ВСЕГДА, на всяком исходе: без него
// «ноль находок» не отличается от «ноль прочитанного».
type Census struct {
	// ProfilesRead — профилей цепочки прочитано.
	ProfilesRead int
	// ServiceDirs — каталогов служб осмотрено.
	ServiceDirs int
	// Manifests — манифестов найдено.
	Manifests int
	// WithoutManifest — службы БЕЗ манифеста, поимённо и по возрастанию.
	//
	// Не находка: манифест заводится своей задачей, и ронять подъём стенда на
	// его отсутствии значило бы делать производителя гейтом чужого предмета.
	// Но и молчать нельзя: доставка пяти манифестов из шести снаружи выглядит
	// точно так же, как доставка всех.
	WithoutManifest []string
	// Bytes — байт манифестов, доставляемых в ConfigMap.
	Bytes int
}

// Delivery — то, что производитель собрал: имя объекта, источники и перепись.
type Delivery struct {
	Name    string
	Sources []Source
	Census  Census
}

// Collect читает объявленное имя из профилей и собирает манифесты дерева.
//
// Перепись возвращается ВСЕГДА, включая отказ: без неё «профили прочитаны, имя
// не объявлено» не отличается от «профили не прочитаны».
func Collect(repoRoot string, profiles []string) (Delivery, error) {
	var d Delivery

	name, read, err := declaredConfigMapName(profiles)
	d.Census.ProfilesRead = read
	if err != nil {
		return d, err
	}
	if name == "" {
		return d, ErrNotDeclared
	}
	d.Name = name

	sources, census, err := discover(repoRoot)
	d.Census.ServiceDirs = census.ServiceDirs
	d.Census.Manifests = census.Manifests
	d.Census.WithoutManifest = census.WithoutManifest
	d.Census.Bytes = census.Bytes
	if err != nil {
		return d, err
	}
	if len(sources) == 0 {
		return d, ErrNoManifests
	}
	if d.Census.Bytes > ConfigMapDataLimit {
		return d, fmt.Errorf(
			"манифестов %d байт при пределе ConfigMap %d — apiserver отвергнет объект целиком; "+
				"это находка производителя, а не повод поднять предел: предел не наш",
			d.Census.Bytes, ConfigMapDataLimit)
	}
	d.Sources = sources
	return d, nil
}

// declaredConfigMapName — имя ConfigMap, объявленное цепочкой профилей.
//
// Профили накладываются СЛЕВА НАПРАВО, ровно как их получает helm: побеждает
// последнее ОБЪЯВЛЕНИЕ, даже если оно пустое. Пустое объявление — это решение
// посадки («доставки здесь нет»), а не молчание, и перебивать его предыдущим
// значило бы принимать решение за оператора.
func declaredConfigMapName(profiles []string) (string, int, error) {
	if len(profiles) == 0 {
		return "", 0, errors.New(
			"цепочка профилей пуста — читать объявление неоткуда; " +
				"пустая цепочка это отказ чтения, а не стенд без профилей")
	}
	name := ""
	read := 0
	for _, p := range profiles {
		// #nosec G304 -- путь приходит от вызывающего (цепочка стенда из
		// stacks.txt); посторонний файл подставить нечем.
		raw, err := os.ReadFile(p)
		if err != nil {
			return "", read, fmt.Errorf("профиль %s не прочитан: %w — непрочитанное есть "+
				"НАХОДКА, а не «объявления нет»", p, err)
		}
		read++
		var tree map[string]any
		if err := yaml.Unmarshal(raw, &tree); err != nil {
			return "", read, fmt.Errorf("профиль %s не разобран: %w", p, err)
		}
		if v, ok := nestedString(tree, chartValuesPath...); ok {
			name = v
		}
	}
	return name, read, nil
}

// nestedString — значение по пути и признак того, что ключ ОБЪЯВЛЕН.
//
// Пустое объявленное значение и отсутствие ключа — разные утверждения, и
// различать их обязан вызывающий.
func nestedString(tree map[string]any, path ...string) (string, bool) {
	cur := any(tree)
	for _, seg := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur, ok = m[seg]
		if !ok {
			return "", false
		}
	}
	if cur == nil {
		return "", true
	}
	s, ok := cur.(string)
	if !ok {
		return "", false
	}
	return s, true
}

// discover — манифесты дерева и перепись осмотренного.
func discover(repoRoot string) ([]Source, Census, error) {
	var census Census
	dir := filepath.Join(repoRoot, servicesDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, census, fmt.Errorf("каталог служб %s не прочитан: %w — "+
			"непрочитанное есть НАХОДКА, а не «служб нет»", dir, err)
	}

	var sources []Source
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		census.ServiceDirs++
		rel := filepath.Join(servicesDir, e.Name(), manifestFileName)
		// #nosec G304 -- путь собран из корня дерева и имени каталога, прочитанного
		// обходом; имя манифеста — константа этого пакета.
		body, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if errors.Is(err, os.ErrNotExist) {
			census.WithoutManifest = append(census.WithoutManifest, e.Name())
			continue
		}
		if err != nil {
			return nil, census, fmt.Errorf("%s: манифест не прочитан: %w", rel, err)
		}
		census.Manifests++
		census.Bytes += len(body)
		sources = append(sources, Source{Dir: e.Name(), Path: rel, Body: body})
	}

	sort.Strings(census.WithoutManifest)
	sort.Slice(sources, func(i, j int) bool { return sources[i].Dir < sources[j].Dir })
	return sources, census, nil
}

// Render собирает объект ConfigMap.
//
// Порядок ключей ЗАКРЕПЛЁН (по каталогу службы): вывод производителя читают
// глазами и сверяют гейтом, а карта Go порядка не имеет — два прогона дали бы
// разный текст при неизменном дереве.
func Render(d Delivery) ([]byte, error) {
	if d.Name == "" {
		return nil, errors.New("имя ConfigMap пусто — собирать нечего")
	}
	if len(d.Sources) == 0 {
		return nil, ErrNoManifests
	}

	data := &yaml.Node{Kind: yaml.MappingNode}
	for _, s := range d.Sources {
		key := &yaml.Node{Kind: yaml.ScalarNode, Value: s.Key()}
		val := &yaml.Node{Kind: yaml.ScalarNode, Value: string(s.Body), Style: yaml.LiteralStyle}
		data.Content = append(data.Content, key, val)
	}

	root := &yaml.Node{Kind: yaml.MappingNode}
	appendScalar(root, "apiVersion", "v1")
	appendScalar(root, "kind", "ConfigMap")

	meta := &yaml.Node{Kind: yaml.MappingNode}
	appendScalar(meta, "name", d.Name)
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "metadata"}, meta)
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "data"}, data)

	root.HeadComment = strings.Join([]string{
		"ПОРОЖДЁН module-manifests-configmap (задача #1901) — руками не править.",
		"Источник: services/*/manifest.yaml того же дерева; имя объекта — из профилей стенда.",
		"Ключ = каталог службы: имя manifest.yaml в одном ConfigMap может быть только одно.",
	}, "\n")

	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("объект ConfigMap не собран: %w", err)
	}
	return out, nil
}

func appendScalar(m *yaml.Node, key, value string) {
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Value: value})
}

// Summary — перепись одной строкой.
func (c Census) Summary() string {
	s := fmt.Sprintf("профилей прочитано %d · каталогов служб %d · манифестов найдено %d · байт %d",
		c.ProfilesRead, c.ServiceDirs, c.Manifests, c.Bytes)
	if len(c.WithoutManifest) > 0 {
		s += " · без манифеста: " + strings.Join(c.WithoutManifest, ", ")
	}
	return s
}
