// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// grant_id_test.go — пробы крючка чеканки идентификатора гранта
// (Config.NewGrantID).
//
// Предмет — ОТКУДА у гранта идентификатор. Его выбирает служба: по нему она
// отзывает семейство и под ним ведёт свою запись семейства, форму которой
// держит ограничение её схемы. Движок, получив запрос без идентификатора,
// чеканит свой при первом чтении — и тогда каждая запись хранилища несёт
// ключ, которого служба не выбирала.
//
// Пробы утверждают НАБЛЮДАЕМОЕ — что лежит в записях подставки хранилища и
// что отдаёт интроспекция, — а не факт вызова крючка. Каждый отказ здесь
// стоит в паре с законным близнецом, отличающимся ровно одним фактом.
package oauthceremony_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/ids"
	"github.com/PRO-Robotech/corelib/oauthceremony"
)

// mintTestGrantID — крючок чеканки, которым собираются церемонии проб. Форма —
// та, что примет схема службы доступа (`tfm-<17>`), и тем генератором, которым
// её чеканит служба.
func mintTestGrantID(context.Context) (string, error) {
	return ids.NewHyphenID(ids.PrefixTokenFamilyHyphen), nil
}

// fixedGrantID — идентификатор, который крючок проб отдаёт по заказу. Форма
// законная, чтобы проба не зависела от того, судит ли кто-то форму.
const fixedGrantID = "tfm-0123456789abcdefg"

// grantIDHook — крючок чеканки, отдающий заказанные идентификаторы по порядку
// и считающий вызовы. Каждый вызов оставляет остаток срока своего контекста.
type grantIDHook struct {
	mu        sync.Mutex
	issue     []string
	fail      error
	calls     int
	remaining []time.Duration
	noLimit   int
}

func (h *grantIDHook) mint(ctx context.Context) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.calls++
	if deadline, limited := ctx.Deadline(); limited {
		h.remaining = append(h.remaining, time.Until(deadline))
	} else {
		h.noLimit++
	}
	if h.fail != nil {
		return "", h.fail
	}
	if len(h.issue) == 0 {
		return "", errors.New("grant id hook: the probe ordered no more identifiers")
	}
	id := h.issue[0]
	h.issue = h.issue[1:]
	return id, nil
}

func (h *grantIDHook) callCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

// withHook ставит крючок пробы в настройки церемонии.
func withHook(hook func(context.Context) (string, error)) func(*oauthceremony.Config) {
	return func(c *oauthceremony.Config) { c.NewGrantID = hook }
}

// storedGrantIDs — идентификаторы гранта во всех записях подставки по видам
// записи. Виды без записей в перечне отсутствуют. Привязка PKCE — поля записи
// кода, а не своя запись, и своего вида у неё нет.
func storedGrantIDs(store *memoryPorts) map[string][]string {
	store.mu.Lock()
	defer store.mu.Unlock()

	out := map[string][]string{}
	for _, row := range store.codes {
		out["код"] = append(out["код"], row.grant.GrantID)
	}
	for _, grant := range store.access {
		out["токен доступа"] = append(out["токен доступа"], grant.GrantID)
	}
	for _, row := range store.refresh {
		out["токен обновления"] = append(out["токен обновления"], row.grant.GrantID)
	}
	for _, v := range out {
		sort.Strings(v)
	}
	return out
}

// requireEveryRecordCarries — каждая запись названных видов несёт want, и
// записей каждого вида НЕ НОЛЬ: «ни одна запись не несёт чужого» на пустом
// хранилище выполнялось бы и без крючка.
func requireEveryRecordCarries(t *testing.T, store *memoryPorts, want string, kinds ...string) {
	t.Helper()

	stored := storedGrantIDs(store)
	for _, kind := range kinds {
		got := stored[kind]
		if len(got) == 0 {
			t.Errorf("ПРЕДПОСЫЛКА: записей вида %q в хранилище нет — утверждать о них нечего", kind)
			continue
		}
		for _, id := range got {
			if id != want {
				t.Errorf("запись вида %q несёт идентификатор гранта %q — ожидался выданный крючком %q", kind, id, want)
			}
		}
	}
}

// ── New ─────────────────────────────────────────────────────────────────────

// TestNewRefusesACeremonyWithoutAGrantIDHook — сборка без крючка отвергается
// ДО первого запроса и называет поле. Близнец — та же сборка с крючком.
func TestNewRefusesACeremonyWithoutAGrantIDHook(t *testing.T) {
	store := newMemoryPorts()
	var cfg oauthceremony.Config
	newTestCeremony(t, store.ports(), func(c *oauthceremony.Config) { cfg = *c })

	if _, err := oauthceremony.New(cfg, store.ports()); err != nil {
		t.Fatalf("ПРЕДПОСЫЛКА: годная сборка с крючком отвергнута: %v", err)
	}

	cfg.NewGrantID = nil
	_, err := oauthceremony.New(cfg, store.ports())
	if !errors.Is(err, oauthceremony.ErrCeremonyMisuse) {
		t.Fatalf("сборка без крючка чеканки принята либо отвергнута не как ошибка сборки: %v", err)
	}
	if !strings.Contains(err.Error(), "Config.NewGrantID") {
		t.Errorf("отказ сборки не называет поле Config.NewGrantID: %v", err)
	}
}

// ── Выдача кода ─────────────────────────────────────────────────────────────

// TestEveryStoredRecordCarriesTheGrantIDTheHookMinted — идентификатор,
// выданный крючком, несут ВСЕ записи семейства: код (с привязкой PKCE в полях
// той же записи), токен доступа и токен обновления — и после обмена, и после
// оборота.
func TestEveryStoredRecordCarriesTheGrantIDTheHookMinted(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	hook := &grantIDHook{issue: []string{fixedGrantID}}
	ceremony := newTestCeremony(t, store.ports(), withHook(hook.mint))

	code, _ := issueCode(t, ceremony)
	requireEveryRecordCarries(t, store, fixedGrantID, "код")

	tokens, err := ceremony.Exchange(context.Background(), codeExchange(code))
	if err != nil {
		t.Fatalf("обмен кода отказал: %v", err)
	}
	requireEveryRecordCarries(t, store, fixedGrantID, "код", "токен доступа", "токен обновления")

	if _, err := ceremony.Exchange(context.Background(), refreshRequest(tokens.RefreshToken)); err != nil {
		t.Fatalf("оборот токена обновления отказал: %v", err)
	}
	requireEveryRecordCarries(t, store, fixedGrantID, "код", "токен доступа", "токен обновления")
	if got := len(storedGrantIDs(store)["токен обновления"]); got != 2 {
		t.Errorf("ПРЕДПОСЫЛКА: после оборота токенов обновления %d, ожидалось 2 — оборот не выпустил пары", got)
	}
}

// TestGrantIDHookIsCalledOncePerGrant — крючок зовётся РОВНО ОДИН раз на грант:
// при выдаче кода. Разбор запроса, отказ в согласии, обмен, оборот,
// интроспекция и отзыв его не зовут, а идентификатор, который отдаёт
// интроспекция и по которому отзывается семейство, — выданный крючком.
func TestGrantIDHookIsCalledOncePerGrant(t *testing.T) {
	const second = "tfm-hjkmnpqrstvwxyz01"

	store := newMemoryPorts()
	registerTestClient(t, store)
	hook := &grantIDHook{issue: []string{fixedGrantID, second}}
	ceremony := newTestCeremony(t, store.ports(), withHook(hook.mint))
	ctx := context.Background()

	denied, err := ceremony.Authorize(ctx, authorizeRequest())
	if err != nil {
		t.Fatalf("Authorize отказал: %v", err)
	}
	if _, err := ceremony.DenyAuthorization(ctx, denied, oauthceremony.ErrAccessDenied); err != nil {
		t.Fatalf("DenyAuthorization отказал: %v", err)
	}
	if got := hook.callCount(); got != 0 {
		t.Fatalf("разбор запроса и отказ в согласии позвали крючок %d раз — гранта они не рождают", got)
	}

	intent, err := ceremony.Authorize(ctx, authorizeRequest())
	if err != nil {
		t.Fatalf("Authorize отказал: %v", err)
	}
	if got := hook.callCount(); got != 0 {
		t.Fatalf("разбор запроса позвал крючок %d раз — гранта он не рождает", got)
	}
	result, err := ceremony.CompleteAuthorization(ctx, intent, loggedIn(oauthceremony.AuthorizationGrant{
		Subject:       testSubject,
		GrantedScopes: []string{"openid", "offline"},
	}))
	if err != nil {
		t.Fatalf("CompleteAuthorization отказал: %v", err)
	}
	if got := hook.callCount(); got != 1 {
		t.Fatalf("выдача кода позвала крючок %d раз, ожидался ровно один", got)
	}

	tokens, err := ceremony.Exchange(ctx, codeExchange(result.Parameters["code"][0]))
	if err != nil {
		t.Fatalf("обмен кода отказал: %v", err)
	}
	if got := grantOf(t, ceremony, tokens.AccessToken); got != fixedGrantID {
		t.Errorf("интроспекция токена доступа называет грант %q — ожидался выданный крючком %q", got, fixedGrantID)
	}
	rotated, err := ceremony.Exchange(ctx, refreshRequest(tokens.RefreshToken))
	if err != nil {
		t.Fatalf("оборот токена обновления отказал: %v", err)
	}
	if got := grantOf(t, ceremony, rotated.AccessToken); got != fixedGrantID {
		t.Errorf("интроспекция токена после оборота называет грант %q — ожидался выданный крючком %q", got, fixedGrantID)
	}
	if err := ceremony.Revoke(ctx, oauthceremony.RevocationRequest{
		Token:        rotated.RefreshToken,
		KindHint:     oauthceremony.TokenKindRefresh,
		ClientID:     testClientID,
		ClientSecret: testSecret,
		AuthMethod:   oauthceremony.ClientAuthBasic,
	}); err != nil {
		t.Fatalf("отзыв токена обновления отказал: %v", err)
	}
	if got := hook.callCount(); got != 1 {
		t.Errorf("обмен, оборот, интроспекция и отзыв позвали крючок: вызовов %d, ожидался один на грант", got)
	}
	requireRevokedFor(t, store, fixedGrantID, oauthceremony.RevocationClientRevoke)
	store.mu.Lock()
	calls := append([]revocationCall(nil), store.revocations...)
	store.mu.Unlock()
	for _, call := range calls {
		if call.grantID != fixedGrantID {
			t.Errorf("порт отзыва получил семейство %q — выданный крючком грант здесь один, %q", call.grantID, fixedGrantID)
		}
	}

	// Второй грант — второй вызов и свой идентификатор.
	issueCode(t, ceremony)
	if got := hook.callCount(); got != 2 {
		t.Errorf("второй грант позвал крючок: вызовов всего %d, ожидалось 2", got)
	}
	codes := storedGrantIDs(store)["код"]
	if want := []string{fixedGrantID, second}; strings.Join(codes, ",") != strings.Join(want, ",") {
		t.Errorf("записи кодов двух грантов несут идентификаторы %v — ожидались %v", codes, want)
	}
}

// TestGrantIDHookRefusalRefusesTheGrantAndStoresNothing — отказ крючка и пустой
// идентификатор — отказ выдачи, и записи в хранилище не появляется. Близнец —
// тот же крючок, отдавший идентификатор.
func TestGrantIDHookRefusalRefusesTheGrantAndStoresNothing(t *testing.T) {
	cases := []struct {
		name     string
		hook     *grantIDHook
		wantErr  *oauthceremony.ProtocolError
		wantKept bool
	}{
		{name: "крючок отказал", hook: &grantIDHook{fail: errors.New("sequence exhausted")}, wantErr: oauthceremony.ErrServerError},
		{name: "крючок отдал пустой идентификатор", hook: &grantIDHook{issue: []string{""}}, wantErr: oauthceremony.ErrPortContract},
		{name: "близнец: крючок отдал идентификатор", hook: &grantIDHook{issue: []string{fixedGrantID}}, wantKept: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports(), withHook(tc.hook.mint))
			ctx := context.Background()

			intent, err := ceremony.Authorize(ctx, authorizeRequest())
			if err != nil {
				t.Fatalf("Authorize отказал: %v", err)
			}
			result, err := ceremony.CompleteAuthorization(ctx, intent, loggedIn(oauthceremony.AuthorizationGrant{
				Subject:       testSubject,
				GrantedScopes: []string{"openid"},
			}))
			if got := tc.hook.callCount(); got != 1 {
				t.Fatalf("выдача кода позвала крючок %d раз, ожидался ровно один", got)
			}

			stored := storedGrantIDs(store)
			if !tc.wantKept {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("выдача кода при негодном исходе крючка ответила %v — ожидался случай %v", err, tc.wantErr)
				}
				var pe *oauthceremony.ProtocolError
				if !errors.As(err, &pe) || !strings.Contains(pe.Debug, "Config.NewGrantID") {
					t.Errorf("отказ не называет в подробностях крючок Config.NewGrantID: %+v", pe)
				}
				if len(result.Parameters["code"]) != 0 {
					t.Errorf("при отказе крючка клиенту выдан код: %v", result.Parameters)
				}
				if len(stored) != 0 {
					t.Errorf("при отказе крючка в хранилище легли записи: %v", stored)
				}
				return
			}
			if err != nil {
				t.Fatalf("близнец: выдача кода отказала: %v", err)
			}
			if got := stored["код"]; len(got) != 1 || got[0] != fixedGrantID {
				t.Errorf("близнец: записи кода %v — ожидалась одна с идентификатором %q", got, fixedGrantID)
			}
		})
	}
}

// TestGrantIDHookCallCarriesThePortDeadline — вызов крючка ограничен сроком
// вызова порта (Config.PortTimeout), а не только сроком всей операции: крючок —
// вызов службы, как и порт, и зависший крючок держал бы выдачу до конца
// операции.
func TestGrantIDHookCallCarriesThePortDeadline(t *testing.T) {
	const portTimeout = 2 * time.Second // newTestCeremony: PortTimeout 2s, OperationTimeout 5s.

	store := newMemoryPorts()
	registerTestClient(t, store)
	hook := &grantIDHook{issue: []string{fixedGrantID}}
	ceremony := newTestCeremony(t, store.ports(), withHook(hook.mint))

	issueCode(t, ceremony)

	hook.mu.Lock()
	defer hook.mu.Unlock()
	if hook.calls != 1 {
		t.Fatalf("выдача кода позвала крючок %d раз, ожидался один", hook.calls)
	}
	if hook.noLimit != 0 || len(hook.remaining) != 1 {
		t.Fatalf("вызов крючка пришёл без срока: без срока %d, со сроком %d", hook.noLimit, len(hook.remaining))
	}
	if left := hook.remaining[0]; left > portTimeout {
		t.Errorf("остаток срока вызова крючка %v больше срока вызова порта %v — крючку назначен срок операции", left, portTimeout)
	}
}

// ── Запись без идентификатора ───────────────────────────────────────────────

// TestStoredGrantWithoutAnIDIsAPortContractBreach — запись, которую хранилище
// отдало живой, но без идентификатора гранта, — нарушение контракта порта, а
// не повод движку начеканить свой: выпущенное или отозванное под таким ключом
// не принадлежало бы ни одному семейству, а отзыв по нему отвечал бы успехом,
// не сняв ничего. Близнец каждого пути — та же запись с идентификатором.
func TestStoredGrantWithoutAnIDIsAPortContractBreach(t *testing.T) {
	type path struct {
		name string
		// run проходит путь; strip велит подставке отдать запись без
		// идентификатора.
		run func(t *testing.T, strip bool) (store *memoryPorts, err error)
	}

	stripped := func(rec oauthceremony.GrantRecord, strip bool) oauthceremony.GrantRecord {
		if strip {
			rec.GrantID = ""
		}
		return rec
	}

	paths := []path{
		{name: "обмен кода", run: func(t *testing.T, strip bool) (*memoryPorts, error) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())
			code, _ := issueCode(t, ceremony)
			// Запись кода — целиком, с её привязкой PKCE: без привязки обмен
			// отказал бы по ней, и проба судила бы не идентификатор.
			var live oauthceremony.AuthorizationCodeRecord
			for signature := range store.codes {
				rec, err := store.readCodeRow(signature)
				if err != nil {
					t.Fatalf("ПРЕДПОСЫЛКА: выданная запись кода не читается: %v", err)
				}
				live = rec
			}
			store.fetchCodeOverride = func(string) (oauthceremony.AuthorizationCodeRecord, error) {
				rec := live
				rec.Grant = stripped(live.Grant, strip)
				return rec, nil
			}
			_, err := ceremony.Exchange(context.Background(), codeExchange(code))
			return store, err
		}},
		{name: "оборот токена обновления", run: func(t *testing.T, strip bool) (*memoryPorts, error) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())
			tokens := exchangeCode(t, ceremony)
			var live oauthceremony.GrantRecord
			for _, row := range store.refresh {
				live = row.grant
			}
			store.fetchRefreshOverride = func(string) (oauthceremony.GrantRecord, error) {
				return stripped(live, strip), nil
			}
			_, err := ceremony.Exchange(context.Background(), refreshRequest(tokens.RefreshToken))
			return store, err
		}},
		{name: "отзыв токена обновления", run: func(t *testing.T, strip bool) (*memoryPorts, error) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())
			tokens := exchangeCode(t, ceremony)
			var live oauthceremony.GrantRecord
			for _, row := range store.refresh {
				live = row.grant
			}
			store.fetchRefreshOverride = func(string) (oauthceremony.GrantRecord, error) {
				return stripped(live, strip), nil
			}
			err := ceremony.Revoke(context.Background(), oauthceremony.RevocationRequest{
				Token:        tokens.RefreshToken,
				KindHint:     oauthceremony.TokenKindRefresh,
				ClientID:     testClientID,
				ClientSecret: testSecret,
				AuthMethod:   oauthceremony.ClientAuthBasic,
			})
			return store, err
		}},
	}

	for _, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			t.Run("без идентификатора", func(t *testing.T) {
				store, err := p.run(t, true)
				if !errors.Is(err, oauthceremony.ErrPortContract) {
					t.Fatalf("запись без идентификатора гранта принята: исход %v, ожидалось нарушение контракта порта", err)
				}
				var pe *oauthceremony.ProtocolError
				if !errors.As(err, &pe) || !strings.Contains(pe.Debug, "GrantRecord.GrantID") {
					t.Errorf("отказ не называет в подробностях поле GrantRecord.GrantID: %+v", pe)
				}
				for kind, got := range storedGrantIDs(store) {
					for _, id := range got {
						if id == "" || !strings.HasPrefix(id, "tfm-") {
							t.Errorf("в хранилище лёг %s под идентификатором %q, которого крючок не выдавал", kind, id)
						}
					}
				}
			})
			t.Run("близнец: с идентификатором", func(t *testing.T) {
				if _, err := p.run(t, false); err != nil {
					t.Fatalf("та же запись с идентификатором отвергнута: %v", err)
				}
			})
		})
	}
}
