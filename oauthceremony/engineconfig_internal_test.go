// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// engineconfig_internal_test.go — методы настроек движка, собранных New, на
// пути запроса не ЗАМЕНЯЮТ значения их полей. Это половина утверждения
// «настройки только читаются»; вторую — запись в содержимое поля — в
// известных формах держит engineconfig_static_internal_test.go.
//
// Геттер настроек движка (internal/oauth2/config_default.go) заполняет
// неназванное поле умолчанием ЛЕНИВО: первым вызовом и без синхронизации.
// Церемония обслуживает одновременные запросы одним значением настроек, и
// ленивая запись из одного запроса против чтения из другого — гонка данных
// (прогон -race волны 27: GetSecretsHasher, config_default.go:261 чтение
// против :262 записи, путь Exchange → AuthenticateClient → checkClientSecret).
//
// Проба судит КЛАСС, а не экземпляр: вызывает КАЖДЫЙ метод настроек, которые
// держит движок собранной церемонии, и называет каждое поле, чьё значение
// вызов ЗАМЕНИЛ. Ленивый геттер, пришедший с обновлением апстрима, краснеет
// здесь без -race и без одновременности.
//
// Слепых зон у сверки две:
//
//   - Геттер, пишущий в поле то же значение, что там уже лежит, сверкой не
//     виден. -race проба одновременных обменов (settings_race_test.go) видит
//     такую запись только как гонку: если запись и чтение поля из
//     одновременных обменов не упорядочены синхронизацией, на пути обмена и в
//     её прогоне. Запись, которую ни один другой обмен не делит с ней без
//     синхронизации, для детектора не гонка, сколько бы раз она ни исполнилась.
//   - Запись в СОДЕРЖИМОЕ ссылочного поля не видна вовсе, даже если поле
//     названо в New: тождество указателя и карты — адрес, среза — адрес,
//     длина и ёмкость, интерфейса — тождество лежащего в нём (identityOf), а
//     запись элемента карты или среза либо поля значения под указателем
//     адреса не меняет. Её держит статическая проба по исходнику
//     TestEngineSettingsMethodsWriteNoFieldContent
//     (engineconfig_static_internal_test.go) — только в тех формах записи,
//     которые знает её разбор; держимые формы и слепая зона разбора — в её
//     шапке. У слепой зоны разбора держателя нет: -race проба судит не форму
//     записи, а только гонку, на тех же условиях, что выше.
//
// Файл внутренний намеренно: настройки движка не видны снаружи ни одним
// элементом пакета.
package oauthceremony

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	engine "github.com/PRO-Robotech/corelib/internal/oauth2"
)

// getterWrite — поле настроек, изменённое вызовом метода.
type getterWrite struct {
	method string
	field  string
}

func (w getterWrite) String() string { return w.method + " → " + w.field }

// getterCensus — перепись одного обхода: сколько осмотрено и что найдено.
// Ноль находок отличим от нуля осмотренного.
type getterCensus struct {
	methods  int      // методов у *engine.Config
	called   int      // вызвано: методы, принимающие только контекст
	fields   int      // полей сверено до и после каждого вызова
	unjudged []string // методы, которые обход вызвать не умеет
	writes   []getterWrite
}

// censusGetterWrites вызывает каждый метод cfg, принимающий только контекст,
// и сверяет поля cfg до и после вызова по тождеству значения.
func censusGetterWrites(cfg *engine.Config) getterCensus {
	ctxType := reflect.TypeFor[context.Context]()
	v := reflect.ValueOf(cfg)
	t := v.Type()
	c := getterCensus{methods: t.NumMethod(), fields: t.Elem().NumField()}

	for i := range t.NumMethod() {
		name := t.Method(i).Name
		mt := v.Method(i).Type()
		if mt.NumIn() != 1 || mt.In(0) != ctxType {
			c.unjudged = append(c.unjudged, name)
			continue
		}
		before := fieldIdentities(cfg)
		v.Method(i).Call([]reflect.Value{reflect.ValueOf(context.Background())})
		c.called++
		after := fieldIdentities(cfg)
		for j := range before {
			if before[j] != after[j] {
				c.writes = append(c.writes, getterWrite{method: name, field: t.Elem().Field(j).Name})
			}
		}
	}
	return c
}

// fieldIdentities — тождество каждого поля cfg: у ссылочных видов — адрес
// (и длина с ёмкостью у среза), у интерфейса — тождество лежащего в нём, у
// прочих — само значение. Ленивая инициализация меняет nil на значение и
// потому видна; запись в содержимое ссылочного поля адреса не меняет и
// потому НЕ видна — её в известных формах судит
// TestEngineSettingsMethodsWriteNoFieldContent.
func fieldIdentities(cfg *engine.Config) []string {
	v := reflect.ValueOf(cfg).Elem()
	out := make([]string, v.NumField())
	for i := range v.NumField() {
		out[i] = identityOf(v.Field(i))
	}
	return out
}

func identityOf(v reflect.Value) string {
	switch v.Kind() {
	case reflect.Func, reflect.Pointer, reflect.Map, reflect.Chan, reflect.UnsafePointer:
		if v.IsNil() {
			return "nil"
		}
		return fmt.Sprintf("%s@%#x", v.Type(), v.Pointer())
	case reflect.Slice:
		if v.IsNil() {
			return "nil"
		}
		return fmt.Sprintf("%s@%#x/%d/%d", v.Type(), v.Pointer(), v.Len(), v.Cap())
	case reflect.Interface:
		if v.IsNil() {
			return "nil"
		}
		return "interface:" + identityOf(v.Elem())
	default:
		return fmt.Sprintf("%s=%#v", v.Type(), v.Interface())
	}
}

// offRequestPath — ленивые геттеры, которых путь запроса церемонии НЕ
// достигает, каждый с предикатом этого утверждения. Исключение истекает
// само: запись, которой нечего исключать, и запись, чей предикат перестал
// держаться, — обе находки.
var offRequestPath = []struct {
	write getterWrite
	// why — почему геттер вне пути запроса.
	why string
	// stillOff — предикат: истина, пока геттер вне пути запроса.
	stillOff func() bool
}{
	{
		write: getterWrite{method: "GetJWKSFetcherStrategy", field: "JWKSFetcherStrategy"},
		why: "набор ключей клиента по адресу движок запрашивает только у клиента, " +
			"реализующего engine.OpenIDConnectClient (утверждение клиента и объект запроса), " +
			"а представление клиента церемонии его не реализует (clientViewOf). Назвать поле " +
			"умолчанием движка значило бы запустить в New фоновый исполнитель кеша ключей " +
			"без способа его остановить — ради пути, которого нет",
		stillOff: func() bool {
			_, oidc := clientViewOf(ClientRegistration{}).(engine.OpenIDConnectClient)
			return !oidc
		},
	},
}

// Порты, которых обход не вызывает: они нужны, чтобы New собрал церемонию.
// Вызов любого из них — разыменование пустого интерфейса, то есть громкое
// красное, а не молчание.
type (
	uncalledClients            struct{ ClientDirectory }
	uncalledAuthorizationCodes struct{ AuthorizationCodeVault }
	uncalledAccessTokens       struct{ AccessTokenVault }
	uncalledRefreshTokens      struct{ RefreshTokenVault }
	uncalledGrants             struct{ GrantRevoker }
	uncalledProofKeys          struct{ ProofKeyVault }
	uncalledAccessTokenIssuer  struct{ AccessTokenIssuer }
)

// engineConfigOfNewCeremony собирает церемонию через New и достаёт настройки,
// которые держит её движок. Предпосылка проверяется, а не предполагается:
// другой вид движка или настроек означал бы, что проба судит не то.
func engineConfigOfNewCeremony(t *testing.T) *engine.Config {
	t.Helper()

	c, err := New(Config{
		Issuer:                          "https://iam.example.net",
		AccessTokenLifespan:             20 * time.Minute,
		RefreshTokenLifespan:            24 * time.Hour,
		AuthorizationCodeLifespan:       10 * time.Minute,
		ScopeMatching:                   ScopeMatchingExact,
		RefreshTokenIssuance:            RefreshTokenIssuanceOnScope,
		RefreshTokenScopes:              []string{"offline"},
		RequireProofKey:                 true,
		RequireProofKeyForPublicClients: true,
		SecretHashCost:                  10,
		MinParameterEntropy:             8,
		PortTimeout:                     2 * time.Second,
		OperationTimeout:                5 * time.Second,
	}, Ports{
		Clients:            uncalledClients{},
		AuthorizationCodes: uncalledAuthorizationCodes{},
		AccessTokens:       uncalledAccessTokens{},
		RefreshTokens:      uncalledRefreshTokens{},
		Grants:             uncalledGrants{},
		ProofKeys:          uncalledProofKeys{},
		AccessTokenIssuer:  uncalledAccessTokenIssuer{},
	})
	if err != nil {
		t.Fatalf("New не собрал церемонию: %v", err)
	}
	provider, ok := c.provider.(*engine.Fosite)
	if !ok {
		t.Fatalf("ПРЕДПОСЫЛКА: движок церемонии — %T, а не *engine.Fosite; проба судила бы не те настройки", c.provider)
	}
	cfg, ok := provider.Config.(*engine.Config)
	if !ok {
		t.Fatalf("ПРЕДПОСЫЛКА: настройки движка — %T, а не *engine.Config; проба судила бы не те настройки", provider.Config)
	}
	return cfg
}

// requireCensusCovered — обход осмотрел непустое и всё: пустой обход не
// вердикт, а метод, который обход не умеет вызвать, — не проверенный метод.
func requireCensusCovered(t *testing.T, c getterCensus) {
	t.Helper()

	t.Logf("перепись: методов %d · вызвано %d · полей сверено %d · записей %d",
		c.methods, c.called, c.fields, len(c.writes))
	if c.methods == 0 || c.fields == 0 {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: обход осмотрел методов %d и полей %d — судить нечего", c.methods, c.fields)
	}
	if len(c.unjudged) != 0 {
		t.Fatalf("методы настроек движка с формой, которую обход вызвать не умеет: %v — "+
			"научи обход их вызывать, иначе ленивая запись в них не видна", c.unjudged)
	}
}

// TestEngineSettingsBuiltByNewAreOnlyReadOnTheRequestPath — ни один метод
// настроек движка собранной церемонии не заменяет значения их поля, кроме
// геттеров, которых путь запроса не достигает, — и те названы с предикатом.
// Запись в содержимое поля в известных формах судит
// TestEngineSettingsMethodsWriteNoFieldContent.
func TestEngineSettingsBuiltByNewAreOnlyReadOnTheRequestPath(t *testing.T) {
	census := censusGetterWrites(engineConfigOfNewCeremony(t))
	requireCensusCovered(t, census)

	found := slices.Clone(census.writes)
	for _, off := range offRequestPath {
		i := slices.Index(found, off.write)
		if i < 0 {
			t.Errorf("исключение %s больше нечего исключать — сними его: геттер не пишет в собранные настройки", off.write)
			continue
		}
		if !off.stillOff() {
			t.Errorf("исключение %s истекло: геттер теперь на пути запроса (%s) — назови поле %s в New",
				off.write, off.why, off.write.field)
		}
		found = slices.Delete(found, i, i+1)
	}
	for _, w := range found {
		t.Error(replacedFieldFindingText(w))
	}
}

// replacedFieldFindingText — текст находки переписи. Он часть свойства:
// средство «назови поле в New» годится только ленивому заполнению пустого поля
// и не годится записи в содержимое, которую перепись не видит.
func replacedFieldFindingText(w getterWrite) string {
	return fmt.Sprintf("геттер движка заменяет значение поля настроек на пути запроса: %s — первая запись "+
		"из одного запроса гоняется с чтением из другого. Если геттер заполняет поле лениво (пишет только "+
		"в пустое), назови поле %s в New; если пишет и в названное, названия мало: запись снимается "+
		"правкой поддерева, а вне пути запроса — исключением offRequestPath с предикатом. Запись в "+
		"СОДЕРЖИМОЕ ссылочного поля эта перепись не видит вовсе: её в известных формах судит "+
		"TestEngineSettingsMethodsWriteNoFieldContent", w, w.field)
}

// TestReplacedFieldFindingTextKeepsNamingInNewToLazyFill — текст находки
// называет запись, ставит «назови поле в New» под условие ленивого заполнения
// и отсылает запись в содержимое к её держателю, а не обещает, что названия
// поля достаточно. Вход — синтетическая запись глубины 1 копии поддерева в
// единице переписи (controlWrite, выведена разбором копии) и ленивые геттеры
// пустых настроек, сколько их есть: их может не остаться — это цель, а не
// «тексту судить нечего».
func TestReplacedFieldFindingTextKeepsNamingInNewToLazyFill(t *testing.T) {
	walkEngineCopy(t)
	census := censusGetterWrites(&engine.Config{})
	requireCensusCovered(t, census)
	inputs := append([]getterWrite{controlWrite}, census.writes...)
	t.Logf("входов тексту: %d (синтетический 1 · ленивых геттеров %d)", len(inputs), len(census.writes))
	for _, w := range inputs {
		text := replacedFieldFindingText(w)
		for _, part := range []string{
			w.String(),
			"лениво (пишет только в пустое), назови поле " + w.field + " в New",
			"названия мало",
			"СОДЕРЖИМОЕ ссылочного поля эта перепись не видит",
			"TestEngineSettingsMethodsWriteNoFieldContent",
		} {
			if !strings.Contains(text, part) {
				t.Errorf("текст находки для %s не называет %q: %s", w, part, text)
			}
		}
		if strings.Contains(text, "поле не названо в New") {
			t.Errorf("текст находки для %s выдаёт «поле не названо в New» за причину: %s", w, text)
		}
	}
}

// TestGetterWriteCensusNamesEveryLazyGetterOfTheEngine — инъекция настоящим
// входом: настройки, в которых не названо НИЧЕГО, — это ровно то, что
// получал бы движок, не назови New ни одного поля. Обход обязан назвать
// каждый ленивый геттер движка поимённо; перечень выведен обходом, а не
// выписан по памяти, и расхождение с ним на обновлении апстрима — повод
// перечитать config_default.go.
func TestGetterWriteCensusNamesEveryLazyGetterOfTheEngine(t *testing.T) {
	census := censusGetterWrites(&engine.Config{})
	requireCensusCovered(t, census)

	want := []getterWrite{
		{method: "GetAudienceStrategy", field: "AudienceMatchingStrategy"},
		{method: "GetJWKSFetcherStrategy", field: "JWKSFetcherStrategy"},
		{method: "GetScopeStrategy", field: "ScopeStrategy"},
		{method: "GetSecretsHasher", field: "ClientSecretsHasher"},
	}
	if !slices.Equal(census.writes, want) {
		t.Fatalf("ленивые геттеры движка: %v, ожидались %v", census.writes, want)
	}
}

// TestGetterWriteCensusIsSilentWhenEveryLazyFieldIsNamed — законный близнец:
// те же настройки, но каждое лениво заполняемое поле названо. Обход молчит —
// значит, красное выше приходит от неназванного поля, а не от самого обхода.
func TestGetterWriteCensusIsSilentWhenEveryLazyFieldIsNamed(t *testing.T) {
	cfg := &engine.Config{
		ScopeStrategy:            engine.ExactScopeStrategy,
		AudienceMatchingStrategy: engine.DefaultAudienceMatchingStrategy,
		JWKSFetcherStrategy:      namedFetcher{},
	}
	cfg.ClientSecretsHasher = &engine.BCrypt{Config: cfg}

	census := censusGetterWrites(cfg)
	requireCensusCovered(t, census)
	if len(census.writes) != 0 {
		t.Fatalf("обход назвал записи в настройках, где всё названо: %v", census.writes)
	}
}

// namedFetcher — непустое значение поля набора ключей для близнеца. Метода
// Resolve у него нет своего: обход геттер вызывает, но набор ключей не
// запрашивает.
type namedFetcher struct{ engine.JWKSFetcherStrategy }
