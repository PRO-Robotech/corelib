// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package validate содержит общие валидаторы полей API Kachō, общие для всех
// сервисов (Folder.Name, Network.Name, Subnet.Name и т. п.).
//
// Все валидаторы возвращают gRPC ошибку `InvalidArgument` с
// `BadRequest.field_violations[]` через `pkg/errors.InvalidArgument()`.
//
// Контракт валидации полей:
//   - Name: единственная форма имени ресурса в дереве — DNS label по RFC 1123,
//     `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$` (строчные буквы, цифры, дефис;
//     первый и последний символ — буква или цифра; 1..63). Пустая строка именем
//     не является: на правке она отвергается `<field> is required`, на создании
//     означает «назови сам» и заменяется NameOrDefault на имя, производное от id.
//   - Description: до 256 символов.
//   - Labels: до 64 пар; ключ `^[a-z][-_./\\@a-z0-9]{0,62}$` (1..63 байта);
//     значение 0..63 байта.
//   - UpdateMask: каждое поле должно быть известно сервисом; неизвестное —
//     `InvalidArgument`.
package validate

import (
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	coreerrors "github.com/PRO-Robotech/corelib/errors"
	"github.com/PRO-Robotech/corelib/ids"
	"github.com/PRO-Robotech/corelib/validate/nameform"
)

// NameForm — единственная форма имени ресурса в дереве (RFC 1123 DNS label).
//
// Сама строка объявлена в `pkg/validate/nameform` — пакете БЕЗ транспорта,
// потому что ту же форму обязан читать слой домена, которому grpc запрещён
// (см. документацию того пакета). Здесь — только ссылка: копии литерала не
// заводится, поэтому расходиться нечему.
const NameForm = nameform.Form

// labelKeyRe — regex ключа label: строчные + цифры + `-_./\@`. Шаблон
// `[a-z][-_./\\@0-9a-z]*`, где `@` входит в character class.
var labelKeyRe = regexp.MustCompile(`^[a-z][-_./\\@a-z0-9]{0,62}$`)

const (
	// MaxNameLen — максимум для Name полей ресурсов.
	MaxNameLen = 63
	// MaxDescriptionLen — лимит описания.
	MaxDescriptionLen = 256
	// MaxLabels — максимальное число label-пар на ресурс.
	MaxLabels = 64
	// MaxLabelKeyLen — длина ключа label.
	MaxLabelKeyLen = 63
	// MaxLabelValueLen — длина значения label.
	MaxLabelValueLen = 63
	// MaxPageSize — верхняя граница для page_size в List RPC.
	MaxPageSize int64 = 1000
	// DefaultPageSize — значение по-умолчанию, когда клиент не задал page_size.
	DefaultPageSize int64 = 50
)

// Name проверяет имя ресурса против единственной формы дерева (NameForm).
//
// Пустая строка — НЕ имя. Она отвергается отдельным сообщением `<field> is
// required`, а не общим сообщением о форме: вызывающий, забывший поле, и
// вызывающий, приславший `My_Name`, ошиблись по-разному, и отказ обязан это
// различать.
//
// На пути СОЗДАНИЯ пустая строка остаётся законным входом — там зовут не эту
// функцию, а пару NameOnCreate (форма) + NameOrDefault (что записать).
func Name(field, value string) error {
	if value == "" {
		return coreerrors.InvalidArgument().
			AddFieldViolation(field, field+" is required").
			Err()
	}
	if !nameform.OK(value) {
		return coreerrors.InvalidArgument().
			AddFieldViolation(field, field+" must match "+NameForm+
				" (lowercase letters, digits, hyphens; starts and ends with a letter or digit; 1..63 chars)").
			Err()
	}
	return nil
}

// defaultNameForID — ЕДИНСТВЕННОЕ производство имени по умолчанию в дереве.
// Намеренно НЕ экспортировано: сервис обязан звать NameOrDefault, а не выводить
// умолчание сам — иначе через месяц в двух сервисах будут две формы умолчания и
// разойдутся они молча, ровно как разошлись четыре регулярки. Единственность
// держит гейт internal/repohygiene TestDefaultNameDerivationIsDeclaredOnce.
//
// Умолчанием служит САМ id, без преобразования, и это не экономия строки:
//
//   - id уже удовлетворяет NameForm by construction — prefix'ы строчные, тело
//     crockford-base32 в строчном алфавите (pkg/ids), разделитель hyphen-формы —
//     дефис в середине. Свойство закреплено пробой по КАЖДОМУ известному prefix'у
//     обеих форм, а не рассуждением;
//   - id глобально уникален by construction, значит производное имя уникально в
//     проекте БЕЗ проверки-перед-вставкой. Любое «посмотреть, занято ли, и взять
//     следующее» — software check-then-act, запрещённый ban #10: под конкуренцией
//     два создания увидят одно и то же свободное имя;
//   - всякая иная форма («resource-<id>», счётчик, случайность) добавила бы второе
//     правило и выбор, на котором сервисы разойдутся.
//
// Остаточный случай назван честно, потому что он не невозможен, а лишь требует
// умысла: NameForm — общее пространство, поэтому арендатор ВПРАВЕ занять именем
// строку, равную id другого своего ресурса в том же проекте. Тогда вставка того
// ресурса вернёт ALREADY_EXISTS — законный отказ, а не порча данных. Разбиения,
// исключающего это by construction, не существует: оно потребовало бы второй
// формы имени, то есть ровно того, что здесь снимается.
func defaultNameForID(id string) string {
	return id
}

// NameOnCreate — форма имени на пути СОЗДАНИЯ: пустая строка законна, непустая
// обязана соответствовать NameForm.
//
// Пустое здесь означает не «имя отсутствует», а «назови сам»: до записи оно
// заменяется умолчанием (NameOrDefault), и ресурс с пустым именем не возникает.
// Требовать имя на создании значило бы ломающее изменение у каждого создающего
// глагола ради величины косметической: адресуется ресурс по неизменяемому id
// (ban #15).
//
// Отвечает на вопрос «допустим ли ввод», а НЕ «что будет записано» — второй
// вопрос задают в момент, когда id уже сгенерирован, и отвечает на него
// NameOrDefault. Два вопроса, два момента, две функции.
func NameOnCreate(field, value string) error {
	if value == "" {
		return nil
	}
	return Name(field, value)
}

// NameOrDefault — имя, которое СЛЕДУЕТ ЗАПИСАТЬ: пустое заменяется умолчанием,
// производным от id.
//
// Зовётся в use-case создания в точке, где id уже сгенерирован. Точка вызова у
// каждого сервиса своя, и это нормально; общей обязана быть функция — правило
// «пустое имя не доживает до записи», рассыпанное по сервисам как
// `if name == "" { name = … }`, разойдётся ровно так же, как разошлись четыре
// регулярки.
//
// Гонки нет by construction: значение известно ДО вставки, вставка одна,
// конфликту взяться неоткуда. Форму value эта функция НЕ проверяет — её
// проверил NameOnCreate на входе.
func NameOrDefault(value, id string) string {
	if value == "" {
		return defaultNameForID(id)
	}
	return value
}

// NameOnUpdate — ЕДИНСТВЕННОЕ решение «что делать с именем на правке».
//
// Возвращает, следует ли записывать имя, и отказ, если присланное недопустимо.
// Пять исходов, и они выведены из того, что proto3 НЕ РАЗЛИЧАЕТ «поле не
// прислано» и «поле пусто»:
//
//	маска пуста, имя пусто          → (false, nil) — полная правка, имени не касались
//	маска пуста, имя непусто        → (true, форма)
//	маска не называет name          → (false, nil)
//	маска называет name, имя пусто  → (false, `<field> is required`)
//	маска называет name, непусто    → (true, форма)
//
// Почему пустая маска НЕ отвергает пустое имя. Полная правка — законный способ
// изменить описание или метки, не трогая имя; отказ там сломал бы каждого, кто
// правит объект целиком. Почему она при этом не ЗАПИСЫВАЕТ пустое: записать
// значило бы вернуть безымянный ресурс той самой дверью, которую закрывает
// отказ строкой ниже, — и упереться в ограничение формы уже на стороне базы,
// отдав вызывающему внутреннюю ошибку вместо контрактного отказа.
//
// Почему функция общая. Правило горизонтально — оно про форму запроса, а не про
// предмет сервиса, и рассыпанное по use-case'ам разойдётся ровно так же, как
// разошлись четыре регулярки. Первая его редакция и завелась копией в одном
// сервисе; здесь она сведена к одной (#715).
func NameOnUpdate(field string, mask []string, value string) (bool, error) {
	named := len(mask) == 0
	for _, m := range mask {
		if m == field {
			named = true
			break
		}
	}
	if !named {
		return false, nil
	}
	if value == "" {
		if len(mask) == 0 {
			// Полная правка: «не прислали», а не «сними имя».
			return false, nil
		}
		// Маска НАЗВАЛА имя, значение пусто — это «сними имя», а снять его нельзя.
		return false, Name(field, value)
	}
	if err := Name(field, value); err != nil {
		return false, err
	}
	return true, nil
}

// Description проверяет длину поля description (UTF-8).
func Description(field, value string) error {
	if utf8.RuneCountInString(value) > MaxDescriptionLen {
		return coreerrors.InvalidArgument().
			AddFieldViolation(field, field+" length exceeds 256 chars").
			Err()
	}
	return nil
}

// Labels проверяет map labels: число пар, длину и regex ключа, длину значения.
func Labels(field string, labels map[string]string) error {
	if len(labels) > MaxLabels {
		return coreerrors.InvalidArgument().
			AddFieldViolation(field, "too many labels (max 64)").
			Err()
	}
	for k, v := range labels {
		if len(k) == 0 || len(k) > MaxLabelKeyLen || !labelKeyRe.MatchString(k) {
			return coreerrors.InvalidArgument().
				AddFieldViolation(field+"."+k, "invalid label key (1..63 chars, lowercase letters, digits, _-./\\@)").
				Err()
		}
		if len(v) > MaxLabelValueLen {
			return coreerrors.InvalidArgument().
				AddFieldViolation(field+"."+k, "label value exceeds 63 chars").
				Err()
		}
	}
	return nil
}

// PageSize проверяет границы page_size в List RPC.
//
// Семантика контракта:
//   - page_size == 0 → допустимо; клиент явно не задал, репозиторий применяет
//     DefaultPageSize. Возвращает (DefaultPageSize, nil).
//   - page_size < 0 или > MaxPageSize → InvalidArgument с FieldViolation;
//     возвращает (0, err). Не silent fallback — это нарушение контракта.
//   - 1..MaxPageSize → возвращает (value, nil).
//
// Возвращаемое effective значение нужно использовать в LIMIT-выражении SQL.
// Каждый репозиторий-метод List должен вызывать PageSize первой строкой
// и пробрасывать err наружу через service.
func PageSize(field string, value int64) (int64, error) {
	if value < 0 || value > MaxPageSize {
		return 0, coreerrors.InvalidArgument().
			AddFieldViolation(field, field+" must be in [0..1000] (0 means default)").
			Err()
	}
	if value == 0 {
		return DefaultPageSize, nil
	}
	return value, nil
}

// ZoneId — format/required-валидация: проверяет, что value не пустой.
//
// Список валидных зон НЕ хардкодится. Existence-валидация (есть ли такая
// зона в БД) — ответственность сервиса, владеющего таблицей `zones`
// (kacho-vpc). Здесь только required-check — формируем единообразный
// FieldViolation для пустого zone_id.
//
// Пустая строка → InvalidArgument c FieldViolation `<field> is required`.
// Непустое значение → nil (caller обязан выполнить existence-check).
func ZoneId(field, value string) error { //nolint:revive // стабильное имя публичного API (потребляется сервисами); переименование — ломающее изменение
	if value == "" {
		return coreerrors.InvalidArgument().
			AddFieldViolation(field, field+" is required").
			Err()
	}
	return nil
}

// Здесь стояли `IPAddress`, `DhcpDomainName` и их регулярное выражение — сняты
// вместе с предметом (vpc-миграция 0029).
//
// Единственным полем, которое их звало, были параметры DHCP подсети: контракт снят
// давно, Go-тип и колонка — этой миграцией. Потребителей в дереве после этого
// осталось НОЛЬ (предикат:
// `grep -rnE '\b(corevalidate|validate)\.(IPAddress|DhcpDomainName)\b' --include='*.go'`
// по всему дереву, кроме сгенерённого `pkg/api`, — пусто; одноимённый
// `domain.IPAddress` у nlb — СВОЙ newtype этого сервиса, другой референт, он жив).
// Экспортированная функция без вызывающих в общем фундаменте — не «на будущее»: она
// приглашает следующий сервис валидировать адрес по правилам снятой подсистемы.

// Здесь стоял набор разрешённых поставщиков защиты от перегрузки — снят по тому же
// признаку и найден по тому же поводу.
//
// Набор был unexported и НЕ имел ни одного читателя: функции, которой он служил, в
// дереве нет вовсе (`git grep 'func DdosProvider'` — пусто), то есть whitelist
// пережил свою проверку. Линтер `unused` назвал его в тот же прогон, что и снятие
// регулярного выражения DNS-имени: класс один — вспомогательное значение, чей
// потребитель снят, остаётся выглядеть действующим правилом.

// UpdateMask проверяет, что все field-ы в mask содержатся в known.
//
// Используется в *.Update методах: каждый сервис указывает свой набор
// разрешенных для апдейта полей; все остальное — InvalidArgument.
func UpdateMask(field string, mask []string, known map[string]struct{}) error {
	for _, f := range mask {
		if _, ok := known[f]; !ok {
			return coreerrors.InvalidArgument().
				AddFieldViolation(field, "unknown field in update_mask: "+f).
				Err()
		}
	}
	return nil
}

// EnvExtraResourceIDPrefixes — имя env-переменной с ДОПОЛНИТЕЛЬНЫМИ известными
// 3-символьными resource-id prefix'ами (comma-separated, напр. "xyz,qqq").
// Читается один раз при инициализации пакета и мёржится в базовый набор.
//
// Назначение — снять blast-radius «новый домен → InvalidArgument на authz-edge
// api-gateway, пока corelib не отредактирован и не перевыпущен»: оператор
// api-gateway задаёт префикс нового семейства через config/env, БЕЗ релиза
// corelib. Базовые платформенные prefix'ы остаются захардкожены (стабильны,
// покрыты регрессионным guard-тестом); config-путь — только расширение вперёд.
const EnvExtraResourceIDPrefixes = "KACHO_EXTRA_RESOURCE_ID_PREFIXES"

// baseResourceIDPrefixes — известные 3-символьные prefix'ы resource-id'ов Kachō,
// стабильное ядро. ЕДИНЫЙ источник — ids.KnownPrefixes() (vpc/nlb/compute/apps/
// registry/iam/legacy префиксы владеет ids), поэтому здесь НЕ дублируется список
// литералов: раньше две независимые копии (ids.knownPrefixes и этот набор) уже
// разошлись (reg/rop были только тут). Расширяется через EnvExtraResourceIDPrefixes
// без правки кода.
var baseResourceIDPrefixes = ids.KnownPrefixes()

// parseResourceIDPrefixes нормализует comma-separated значение env в список
// значимых prefix'ов: обрезает пробелы, приводит к нижнему регистру, отбрасывает
// пустые и НЕ-ровно-3-символьные токены (family-agnostic ResourceID сверяет
// только первые 3 символа id — токен иной длины никогда не сматчился бы, поэтому
// молча его игнорируем как невалидную конфигурацию).
func parseResourceIDPrefixes(csv string) []string {
	if strings.TrimSpace(csv) == "" {
		return nil
	}
	var out []string
	for _, tok := range strings.Split(csv, ",") {
		p := strings.ToLower(strings.TrimSpace(tok))
		if len(p) != 3 {
			continue
		}
		out = append(out, p)
	}
	return out
}

// buildResourceIDPrefixes копирует базовый набор и мёржит в него extra-prefix'ы
// из config (comma-separated csv). Чистая функция — тестируется без env.
func buildResourceIDPrefixes(csv string) map[string]struct{} {
	m := make(map[string]struct{}, len(baseResourceIDPrefixes)+4)
	for k := range baseResourceIDPrefixes {
		m[k] = struct{}{}
	}
	for _, p := range parseResourceIDPrefixes(csv) {
		m[p] = struct{}{}
	}
	return m
}

// resourceIDPrefixes — эффективный набор (base + config-extras), собранный один
// раз при инициализации пакета.
var resourceIDPrefixes = buildResourceIDPrefixes(os.Getenv(EnvExtraResourceIDPrefixes))

// EnvExtraResourceIDHyphenPrefixes — имя env-переменной с ДОПОЛНИТЕЛЬНЫМИ
// hyphen-form prefix'ами (comma-separated, напр. "foo,bar" — сам prefix БЕЗ
// дефиса). Параллель к EnvExtraResourceIDPrefixes, но для going-forward формы
// "<prefix>-<base32>" (B3). Назначение то же: новый домен на hyphen-канон
// маршрутизируется на authz-edge api-gateway БЕЗ релиза corelib — оператор
// задаёт prefix через config. Канонические platform-prefix'ы (см.
// ids.KnownHyphenPrefixes) остаются захардкожены; config-путь — только
// расширение вперёд.
const EnvExtraResourceIDHyphenPrefixes = "KACHO_EXTRA_RESOURCE_ID_HYPHEN_PREFIXES"

// baseHyphenPrefixes — канонические going-forward hyphen-form prefix'ы Kachō
// (B3). ЕДИНЫЙ источник — ids.KnownHyphenPrefixes() (тот же принцип, что и
// baseResourceIDPrefixes ← ids.KnownPrefixes()): здесь НЕ дублируется список
// литералов. Расширяется через EnvExtraResourceIDHyphenPrefixes без правки кода.
var baseHyphenPrefixes = ids.KnownHyphenPrefixes()

// parseHyphenPrefixes нормализует comma-separated env в список hyphen-form
// prefix'ов: обрезает пробелы, приводит к нижнему регистру, отбрасывает пустые
// и токены, СОДЕРЖАЩИЕ дефис (дефис — id-разделитель, не часть prefix'а).
// В отличие от parseResourceIDPrefixes НЕ навязывает длину 3 — hyphen-prefix
// бывает 2-символьным (`ns`/`mt`/`vt`).
func parseHyphenPrefixes(csv string) []string {
	if strings.TrimSpace(csv) == "" {
		return nil
	}
	var out []string
	for _, tok := range strings.Split(csv, ",") {
		p := strings.ToLower(strings.TrimSpace(tok))
		if p == "" || strings.ContainsRune(p, '-') {
			continue
		}
		out = append(out, p)
	}
	return out
}

// buildHyphenPrefixes копирует канонический hyphen-набор и мёржит config-extras.
// Чистая функция — тестируется без env.
func buildHyphenPrefixes(csv string) map[string]struct{} {
	m := make(map[string]struct{}, len(baseHyphenPrefixes)+4)
	for k := range baseHyphenPrefixes {
		m[k] = struct{}{}
	}
	for _, p := range parseHyphenPrefixes(csv) {
		m[p] = struct{}{}
	}
	return m
}

// hyphenResourceIDPrefixes — эффективный hyphen-набор (canon + config-extras),
// собранный один раз при инициализации пакета.
var hyphenResourceIDPrefixes = buildHyphenPrefixes(os.Getenv(EnvExtraResourceIDHyphenPrefixes))

// ResourceID проверяет, что resource-id синтаксически валиден — начинается с
// известного prefix Kachō в ОДНОЙ из двух форм (B3, redesign-2026):
//
//   - legacy слитная форма "<prefix><17-crockford-base32>" — первые 3 символа ∈
//     resourceIDPrefixes (`net…`, `epd…`, `acb…`);
//   - going-forward hyphen-форма "<prefix>-<crockford-base32>" — сегмент ДО
//     первого дефиса ∈ hyphenResourceIDPrefixes (`ins-…`, `ns-…`, `mt-…`).
//
// Крокфорд-тело дефис не содержит, поэтому наличие дефиса — однозначный сигнал
// новой формы; классификация **строго аддитивна** — legacy-поведение не
// меняется (hyphen-приём только ДОБАВЛЯЕТ acceptance, а не отзывает). Сервисы
// мигрируют свой prefix по одному, поэтому router обязан принимать обе формы в
// переходный период. Пустой id — пропускается (required-проверка /
// transcoding-роутинг — отдельно).
//
// Контракт: на malformed / нераспознанный resource-id мутирующие и read-RPC
// отдают sync `InvalidArgument` с flat-message `"invalid <resourceType> id '<id>'"`
// (НЕ `NotFound`). Семантика **family-agnostic**: prefix должен быть из известного
// набора, но НЕ обязан совпадать с типом ресурса (`enp`-id, переданный как
// subnet-id, проходит → дальше `repo.Get` → `NotFound`). Длину/алфавит тела
// внутри здесь не проверяем.
//
//	resourceType   — имя ресурса в нижнем регистре ("network", "subnet",
//	                 "security group", "folder", "gateway", "private endpoint", ...).
//	expectedPrefix — prefix семейства этого ресурса (ids.PrefixNetwork и т.п.).
//	                 По контракту проверка family-agnostic (см. выше), поэтому
//	                 значение осознанно НЕ сверяется с id — параметр документирует
//	                 в call-site'е, какое семейство ожидается, не навязывая strict-
//	                 сверку. Это не «зарезервировано на будущее»: family-agnostic —
//	                 конечный контракт (enp-id как subnet-id должен доходить до
//	                 repo.Get → NotFound, а не отбиваться InvalidArgument здесь).
//
// Возвращаемая ошибка — готовый gRPC `status` с нужным flat-message (не
// field-violation builder — в этом случае контракт требует flat-message).
func ResourceID(resourceType, expectedPrefix, id string) error {
	_ = expectedPrefix // family-agnostic по контракту — см. doc выше.
	if id == "" {
		return nil
	}
	// Going-forward hyphen-форма (аддитивно): сегмент до первого дефиса ∈ канон.
	// hy>0 отсекает id, начинающийся с дефиса (пустой prefix).
	if hy := strings.IndexByte(id, '-'); hy > 0 {
		if _, ok := hyphenResourceIDPrefixes[id[:hy]]; ok {
			return nil
		}
	}
	// Legacy слитная форма (без изменений): первые 3 символа ∈ канон.
	if len(id) >= 3 {
		if _, ok := resourceIDPrefixes[id[:3]]; ok {
			return nil
		}
	}
	return status.Errorf(codes.InvalidArgument, "invalid %s id '%s'", resourceType, id)
}
