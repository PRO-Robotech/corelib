// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package grpcsrv — cert_identity.go: client-cert identity extractor + the
// principal⟺mTLS trust invariant.
//
// Two orthogonal server-side identities coexist on a cluster-internal listener
// and are BOTH made available downstream (for audit):
//
//   - cert-identity (the *module*): an unmodified, opaque SPIFFE-like SAN string
//     extracted from the verified client-cert presented over mTLS. This layer
//     only extracts the string; it does NOT parse the sva-id, validate it against
//     IAM, or resolve it to a ServiceAccount.
//   - principal (the *user*): carried in x-kacho-principal-* metadata, set by the
//     api-gateway auth-interceptor after JWT validation (see principal_extract.go).
//
// Trust invariant: on an mTLS listener, incoming principal-metadata is trusted ⟺
// the peer passed mTLS client-cert verification from the internal CA. With no
// verified client-cert, principal-metadata from that peer is NOT trusted (and
// must be dropped by the authz layer). On an insecure listener (enable=false,
// dev-mode) the invariant is inapplicable — there is no client-cert at all and
// principal-metadata is accepted as today (backward-compat). The invariant
// activates only under mTLS.
//
// The SAN format is the SPIRE-compatible internal trust-domain form
// spiffe://<trust-domain>/ns/<ns>/sa/kacho-<svc>; cert-manager issues string-SANs
// in this exact shape. The trust domain is NAMED BY THE INSTALLATION
// ([TrustDomain]), not compiled in: only URIs under the declared domain are
// accepted, and a foreign spiffe trust-domain yields empty (no foreign-field
// leak). An installation that did not name its domain recognizes nobody — see
// [TrustDomain] on why the zero value is the strictest reading available.
package grpcsrv

import (
	"context"
	"crypto/x509"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	"github.com/PRO-Robotech/corelib/operations"
)

// trustedPrincipalConfig — конфиг trust-aware principal-extract'а.
type trustedPrincipalConfig struct {
	// forwarders — allow-list cert-identity SAN'ов, которым разрешено форвардить
	// end-user principal-metadata (обычно единственный — api-gateway SA). Пусто →
	// доверяем любому mTLS-verified peer'у (backward-compat) с WARN-предупреждением.
	forwarders map[string]struct{}
	// arrival — счётчик того, что запрос сказал о личности (identity_arrival.go).
	// Нулевой прозрачен: пробе метрики не нужны, развёрнутому слушателю их
	// ставит носитель контура.
	arrival *IdentityArrival
}

// TrustedPrincipalOption — функциональная опция UnaryTrustedPrincipalExtract.
type TrustedPrincipalOption func(*trustedPrincipalConfig)

// WithTrustedForwarders ограничивает форвард end-user principal'а КРУГОМ
// доверенных отправителей (api-gateway и те, кто законно говорит за инициатора).
// Когда круг сужен, principal форвардится ТОЛЬКО если cert-identity peer'а ∈ круг —
// иначе principal снимается (defense-in-depth против confused-deputy: внутренний
// сервис со своим валидным mTLS-cert'ом не может выдать себя за пользователя).
// Несуженный круг (нулевое значение [TrustedForwarders]) сохраняет прежнее
// поведение «любой verified peer доверен».
//
// Эта семантика НЕ изменилась вместе с вводом типа и меняться не должна: её уже
// читают все, включая тех, кто про тип ничего не знает. От «забыл заполнить»
// защищает отказ старта у каждого, кто сужает, — а не переопределение смысла
// пустого множества здесь.
//
// Аргумент — ТИП, а не срез строк, намеренно: круг обязан приезжать сюда ровно
// тем значением, которое одобрила стража старта и о котором отчитался самоотчёт
// о посадке. Сырой срез компилятор здесь больше не примет, поэтому «стража
// считала одно, транспорт получил другое» стало невыразимым.
func WithTrustedForwarders(f TrustedForwarders) TrustedPrincipalOption {
	return func(c *trustedPrincipalConfig) {
		sans := f.SANs()
		if c.forwarders == nil {
			c.forwarders = make(map[string]struct{}, len(sans))
		}
		for _, s := range sans {
			c.forwarders[s] = struct{}{}
		}
	}
}

// CertIdentity extracts the module identity from a (verified) client-cert as the
// unmodified, opaque SPIFFE-like SAN string. Selection rule (part of the
// extractor contract): the FIRST URI-SAN belonging to THIS trust domain is
// returned exactly as it appears in the cert; other URI-SANs are ignored and the
// result is stable across calls.
//
// Метод, а не функция пакета, намеренно: личность существует ОТНОСИТЕЛЬНО
// домена, и вопрос «наш ли этот предъявитель» без домена не задаётся. Пока это
// была функция, домен приезжал скомпилированной константой, то есть ответ на
// него был один на все установки.
//
// Returns "" deterministically when cert is nil, has no URI-SANs, or has no
// URI-SAN under the declared trust domain — and ALWAYS when the domain itself is
// not declared ([TrustDomain.Matches] recognizes nobody then). It never parses or
// resolves the identity and never panics.
func (d TrustDomain) CertIdentity(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	for _, u := range cert.URIs {
		if u == nil {
			continue
		}
		if s := u.String(); d.Matches(s) {
			return s
		}
	}
	return ""
}

// certIdentityCtxKey is a private context key carrying the extracted module
// identity and whether the peer was mTLS-verified.
type certIdentityCtxKey struct{}

type certIdentity struct {
	id       string
	verified bool
	// domain — домен, ОТНОСИТЕЛЬНО которого личность признана нашей. Хранится
	// рядом с ней намеренно: вопрос «наш ли предъявитель» без домена не задаётся,
	// а читатели ниже по цепочке разбирают эту же строку и обязаны спрашивать ТОТ
	// ЖЕ домен, что её впустил. Величина, взятая ими откуда-то ещё, была бы вторым
	// местом об одном предмете.
	domain TrustDomain
}

// WithCertIdentityIn stores the extracted cert-identity, the domain it was
// recognized under, and the mTLS-verified flag in ctx. Exposed so the
// principal-aware layer (and tests) can assert the invariant deterministically
// without a live TLS peer.
//
// # Почему домен здесь ОБЯЗАТЕЛЕН, а формы без него не существует
//
// Личность существует ОТНОСИТЕЛЬНО домена, и читатели ниже по цепочке
// (`authzguard.SANToServiceDomain` и соседи) разбирают эту же строку, спрашивая
// домен у контекста. Контекст, собранный без домена, отдал бы им нулевое
// значение — и они fail-closed отвергли бы личность, которую сами же признали
// нашей. Отказ при этом выглядел бы как отсутствие прав у законного модуля.
//
// Форма с двумя аргументами существовала и была снята: она собиралась молча и
// давала ровно такой контекст. Запретить её значением было нечем — запретили
// подписью.
func WithCertIdentityIn(ctx context.Context, d TrustDomain, id string, verified bool) context.Context {
	return context.WithValue(ctx, certIdentityCtxKey{}, certIdentity{id: id, verified: verified, domain: d})
}

// CertIdentityFromContext returns the extracted module identity and whether the
// peer was mTLS-verified. A ctx that never carried a cert-identity (no mTLS peer)
// reports ("", false) — i.e. NOT mTLS-verified (default-deny of trust).
func CertIdentityFromContext(ctx context.Context) (id string, verified bool) {
	if ctx == nil {
		return "", false
	}
	if v, ok := ctx.Value(certIdentityCtxKey{}).(certIdentity); ok {
		return v.id, v.verified
	}
	return "", false
}

// CertIdentityDomainFromContext returns the trust domain the cert-identity on
// ctx was recognized under.
//
// Существует затем, чтобы читатель личности НИЖЕ по цепочке разбирал её
// относительно ТОГО ЖЕ домена, который её впустил. Домен, взятый им из своей
// настройки, был бы вторым местом об одном предмете: две величины совпадают
// сегодня и разъезжаются молча — а расхождение здесь означает либо отказ
// законному модулю, либо признание чужого.
//
// Нулевой домен у контекста, никогда не проходившего извлекатель, — фейл-клоуз:
// по необъявленному домену не опознаётся никто.
func CertIdentityDomainFromContext(ctx context.Context) TrustDomain {
	if ctx == nil {
		return TrustDomain{}
	}
	if v, ok := ctx.Value(certIdentityCtxKey{}).(certIdentity); ok {
		return v.domain
	}
	return TrustDomain{}
}

// peerTLSState classifies the transport security of the incoming peer:
//   - tlsPresent: true when the connection is TLS (one-way or mutual), false on
//     an insecure (plaintext) listener.
//   - verifiedCert: the first verified client-cert leaf, or nil when the peer
//     presented no client-cert OR none verified against the client-CA.
func peerTLSState(ctx context.Context) (tlsPresent bool, verifiedCert *x509.Certificate) {
	p, ok := peer.FromContext(ctx)
	if !ok || p.AuthInfo == nil {
		return false, nil
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return false, nil
	}
	// TLS is present. A verified client-cert appears as the leaf of the first
	// verified chain; absence ⇒ no verified client-cert reached us.
	if len(tlsInfo.State.VerifiedChains) > 0 && len(tlsInfo.State.VerifiedChains[0]) > 0 {
		return true, tlsInfo.State.VerifiedChains[0][0]
	}
	return true, nil
}

// UnaryCertIdentityExtract is a server interceptor that classifies the peer's
// transport security and, for an mTLS-verified peer, extracts its module
// identity into ctx. It MUST run before UnaryTrustedPrincipalExtract.
//
//   - mTLS-verified peer → WithCertIdentity(ctx, CertIdentity(leaf), true).
//   - TLS peer without a verified client-cert → WithCertIdentity(ctx, "", false)
//     (defense-in-depth: marks the peer not-verified so principal is dropped).
//   - insecure (plaintext) peer → ctx untouched (no cert-identity ever set);
//     CertIdentityFromContext then reports ("", false) and the principal layer
//     treats the insecure listener as dev backward-compat.
func UnaryCertIdentityExtract(d TrustDomain) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx = withCertIdentityFromPeer(d, ctx)
		return handler(ctx, req)
	}
}

// StreamCertIdentityExtract is the stream analogue of UnaryCertIdentityExtract.
func StreamCertIdentityExtract(d TrustDomain) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx := withCertIdentityFromPeer(d, ss.Context())
		return handler(srv, &certIdentityStream{ServerStream: ss, ctx: ctx})
	}
}

type certIdentityStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *certIdentityStream) Context() context.Context { return s.ctx }

func withCertIdentityFromPeer(d TrustDomain, ctx context.Context) context.Context {
	tlsPresent, leaf := peerTLSState(ctx)
	if !tlsPresent {
		// Insecure listener: no client-cert at all. Leave ctx untouched so the
		// principal layer applies dev backward-compat (invariant N/A).
		return ctx
	}
	if leaf == nil {
		// TLS but no verified client-cert (defense-in-depth). RequireAndVerify
		// normally rejects this at handshake; if it ever reaches here, mark the
		// peer not-verified so principal-metadata is dropped.
		return WithCertIdentityIn(ctx, d, "", false)
	}
	return WithCertIdentityIn(ctx, d, d.CertIdentity(leaf), true)
}

// UnaryTrustedPrincipalExtract reads x-kacho-principal-* metadata and exposes it
// downstream ONLY when it is trustworthy under the trust invariant. It MUST run
// after UnaryCertIdentityExtract.
//
// Trust decision:
//   - mTLS-verified peer (CertIdentityFromContext verified=true) → principal
//     trusted.
//   - insecure listener (no cert-identity ever set on ctx) → principal trusted as
//     today, dev backward-compat. Distinguished from an unverified TLS peer by
//     peerTLSState: insecure ⇒ no TLS transport at all.
//   - TLS peer without a verified client-cert → principal NOT trusted; dropped
//     (defense-in-depth).
//
// Trust answers "may this peer speak for a user", not "did this request name
// one". A trusted peer that forwarded NO principal-metadata leaves the request
// principal-less: the standard carrier reports no principal, and no identity is
// invented for it. Anything else would hand the system identity — the one the
// ownership predicate matches on every system-written operation — to a request
// that presented no credential at all.
//
// The decision is recorded via withTrustedPrincipal so TrustedPrincipalFromContext
// returns (principal, trusted). cert-identity and principal are orthogonal and
// both remain available downstream for audit — neither substitutes the other.
func UnaryTrustedPrincipalExtract(opts ...TrustedPrincipalOption) grpc.UnaryServerInterceptor {
	cfg := buildTrustedPrincipalConfig(opts)
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, err := withTrustedPrincipal(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// StreamTrustedPrincipalExtract is the stream analogue.
func StreamTrustedPrincipalExtract(opts ...TrustedPrincipalOption) grpc.StreamServerInterceptor {
	cfg := buildTrustedPrincipalConfig(opts)
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := withTrustedPrincipal(ss.Context(), cfg)
		if err != nil {
			return err
		}
		return handler(srv, &certIdentityStream{ServerStream: ss, ctx: ctx})
	}
}

func buildTrustedPrincipalConfig(opts []TrustedPrincipalOption) trustedPrincipalConfig {
	var cfg trustedPrincipalConfig
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

type trustedPrincipalCtxKey struct{}

type trustedPrincipal struct {
	principal operations.Principal
	// acr — the forwarded JWT `acr`. Carried ONLY when trusted (same boundary as
	// principal); empty on an untrusted/unverified peer.
	acr     string
	trusted bool
}

// withTrustedPrincipal решает, чью личность несёт запрос, и ОТВЕРГАЕТ его, когда
// личность была объявлена и не приехала (identity_arrival.go).
//
// Отказ живёт здесь, а не слоем ниже, потому что «объявлена» — утверждение
// ЭТОГО слоя: он единственный знает, какие имена читает эта сборка и чью
// пересылку слушатель почитает. Слой прав отверг бы такой запрос лишь на тех
// записях каталога, где вопрос о субъекте задаётся, — а освобождённых от него
// записей в каталоге хватает, и на них наследовать нечего.
func withTrustedPrincipal(ctx context.Context, cfg trustedPrincipalConfig) (context.Context, error) {
	trusted := principalIsTrusted(ctx, cfg)
	// p is whatever the peer FORWARDED — nothing when it forwarded nothing. It is
	// deliberately NOT seeded with the system principal: trust is a property of
	// the FORWARDER (this peer may speak for a user), never a property of the
	// request (this request named someone). A trusted peer is trusted while it
	// forwards nothing too, so seeding would hand the system identity to a
	// request that presented none — and that identity is the one the ownership
	// predicate matches on every system-written operation.
	p, forwarded := principalFromIncomingMetadata(ctx, defaultDebugConfig())
	acr := ""
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		acr = first(md.Get(MDKeyTokenACR))
	}
	switch {
	case !trusted:
		// On an unverified / non-forwarder peer the forwarded principal-metadata is
		// dropped — and so is the acr (anti-spoof; a non-gateway peer cannot elevate
		// its acr by forging the header).
		acr = ""
		// Scrub any pre-set principal carrier so a forged/leftover principal from
		// an untrusted peer never reaches use-cases (defense-in-depth).
		ctx = operations.WithoutPrincipal(ctx)
	case forwarded:
		// Make the trusted principal available to the standard operations carrier
		// so existing use-cases (operations.PrincipalFromContext) see it too.
		ctx = operations.WithPrincipal(ctx, p)
	default:
		// Trusted peer, but the request carried no identity at all. Record the
		// absence: the carrier must report "no principal", not a fabricated one.
		ctx = operations.WithoutPrincipal(ctx)
	}
	// Исход наблюдается ВСЕГДА, включая законную безымянность: без неё «личность
	// объявлена и не приехала» неотличимо от роста безымянных вызовов, то есть
	// не видно ничем.
	outcome := arrivalUntrusted
	if trusted {
		outcome = classifyIdentityArrival(ctx, forwarded)
	}
	cfg.arrival.observe(outcome)
	if outcome.refused() {
		return ctx, refusalFor(outcome)
	}
	return context.WithValue(ctx, trustedPrincipalCtxKey{}, trustedPrincipal{principal: p, acr: acr, trusted: trusted}), nil
}

// principalIsTrusted implements the trust decision (see UnaryTrustedPrincipalExtract).
func principalIsTrusted(ctx context.Context, cfg trustedPrincipalConfig) bool {
	tlsPresent, _ := peerTLSState(ctx)
	if !tlsPresent {
		// Insecure listener: dev backward-compat, principal accepted as today.
		return true
	}
	// TLS listener: peer must be mTLS client-cert verified.
	certID, verified := CertIdentityFromContext(ctx)
	if !verified {
		return false
	}
	// If a forwarder allow-list is configured, the verified peer must also be a
	// recognised forwarder (api-gateway) to forward an end-user principal —
	// otherwise any internal service with a valid cert could impersonate a user.
	if len(cfg.forwarders) > 0 {
		_, ok := cfg.forwarders[certID]
		return ok
	}
	return true
}

// TrustedPrincipalFromContext returns the principal and whether it is trusted
// under the trust invariant. trusted=false means the principal-metadata came from
// an unverified peer on an mTLS listener and the authz layer must ignore it.
//
// The returned principal is the ZERO Principal when the peer forwarded none,
// and likewise when ctx never carried the trust decision at all — trusted=true
// then says "this forwarder is recognised", not "someone was named". Callers
// must treat an empty Type/ID as "no principal" and never as an identity to
// compare against (operations.Principal.IsAnonymous is the shared predicate).
// The absent-carrier case deliberately does NOT fall back to the system
// principal: pairing a real-looking value with trusted=false only protects
// callers who read the flag, and the value would be the very identity the
// operation-ownership predicate honours everywhere.
func TrustedPrincipalFromContext(ctx context.Context) (operations.Principal, bool) {
	if ctx == nil {
		return operations.Principal{}, false
	}
	if v, ok := ctx.Value(trustedPrincipalCtxKey{}).(trustedPrincipal); ok {
		return v.principal, v.trusted
	}
	return operations.Principal{}, false
}

// WithTrustedACR stores a forwarded JWT `acr` and the trust flag directly in
// ctx (bypassing the metadata extract). Exposed so the iam acr-floor and tests
// can assert the floor deterministically without a live mTLS peer — the mirror of
// WithCertIdentity for the principal/acr layer. Note: this overwrites any existing
// trusted-principal carrier's acr/trusted with the given values while keeping the
// principal as previously recorded — and leaving it EMPTY when none was: an acr
// says how strongly someone authenticated, never who they are, so this
// constructor has no business naming anyone.
func WithTrustedACR(ctx context.Context, acr string, trusted bool) context.Context {
	var tp trustedPrincipal
	if v, ok := ctx.Value(trustedPrincipalCtxKey{}).(trustedPrincipal); ok {
		tp = v
	}
	tp.acr = acr
	tp.trusted = trusted
	return context.WithValue(ctx, trustedPrincipalCtxKey{}, tp)
}

// WithTrustedPrincipal stores a forwarded principal and the trust flag directly
// in ctx (bypassing the metadata extract) — the principal-side mirror of
// WithTrustedACR, exposed for the same reason: so the iam floors and their tests
// can drive a machine-vs-user caller deterministically without a live mTLS peer.
// It keeps any acr already recorded on the carrier and overwrites the principal
// and the trust flag.
//
// Note the asymmetry it preserves from the real extract path: on an untrusted
// peer the forwarded PRINCIPAL is still recorded (only `trusted` goes false),
// whereas the acr is scrubbed. Consumers must therefore consult the trusted flag
// before believing the principal type — a forged `service_account` from an
// unverified peer must never buy the step-up exemption.
func WithTrustedPrincipal(ctx context.Context, p operations.Principal, trusted bool) context.Context {
	var tp trustedPrincipal
	if v, ok := ctx.Value(trustedPrincipalCtxKey{}).(trustedPrincipal); ok {
		tp = v
	}
	tp.principal = p
	tp.trusted = trusted
	return context.WithValue(ctx, trustedPrincipalCtxKey{}, tp)
}

// TrustedACRFromContext returns the forwarded JWT `acr` and whether it is trusted
// under the trust invariant. trusted=false means the acr came from an unverified
// peer on an mTLS listener (or no acr was carried) and an acr-floor must treat it
// as absent (rank 0, fail-closed). On the insecure dev listener the acr is
// accepted as today (back-compat), consistent with the principal.
func TrustedACRFromContext(ctx context.Context) (acr string, trusted bool) {
	if ctx == nil {
		return "", false
	}
	if v, ok := ctx.Value(trustedPrincipalCtxKey{}).(trustedPrincipal); ok {
		return v.acr, v.trusted
	}
	return "", false
}
