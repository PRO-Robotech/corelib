// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package main

// Пробы полосы X2 NTF-3 (приёмка sub-phase-NTF-3, отпечаток ac1f9fc9…) на
// Go-половине генератора: форма адресата шаблона в описании и в SendX (Р27),
// форма fanout без адресата и с name optional (Р3, З10 п.3, З17), ключ окна
// «проект» (Р14). Каждое порождённое собирается против feed этого дерева: проба
// судит собранный код, а не только его текст.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const inviteBodyEN = `blocks:
  - heading: "You are invited"
  - p: "{{ inviter_name }} invites you to the cloud."
  - button: {text: "Accept the invitation", token: token, path: "/iam/invitations/accept"}
`

// x2Tree — дерево A01 на обеих локалях набора {ru, en} (Р21).
func x2Tree(t *testing.T) *tree {
	t.Helper()
	tr := newTree(t)
	tr.write("svc/notifications/invite/body.en.yaml", inviteBodyEN)
	tr.edit("svc/notifications/invite/notification.yaml", `  ru: "Приглашение в облако"`,
		"  ru: \"Приглашение в облако\"\n  en: \"Invitation to the cloud\"")
	return tr
}

// buildAgainstFeed собирает порождённое дерево против corelib этого дерева.
func buildAgainstFeed(t *testing.T, tr *tree) {
	t.Helper()
	corelib, err := filepath.Abs("../..")
	require.NoError(t, err)
	tr.write("go.mod", "module example.invalid/src\n\ngo 1.26.0\n\nrequire github.com/PRO-Robotech/corelib v0.0.0\n\n"+
		"replace github.com/PRO-Robotech/corelib => "+corelib+"\n")
	sum, err := os.ReadFile(filepath.Join(corelib, "go.sum"))
	require.NoError(t, err)
	tr.write("go.sum", string(sum))
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = tr.root
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off", "GOWORK=off")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "сборка порождённого: %s", out)
}

// attrFields — поля структуры <Name>Attrs порождённого файла.
func attrFields(t *testing.T, src, typeName string) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "gen.go", src, 0)
	require.NoError(t, err)
	fields := map[string]string{}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if x, ok := n.(*ast.TypeSpec); ok && x.Name.Name == typeName {
			found = true
			for _, fl := range x.Type.(*ast.StructType).Fields.List {
				for _, nm := range fl.Names {
					fields[nm.Name] = types(fl.Type)
				}
			}
		}
		return true
	})
	require.True(t, found, "типа %s в порождённом нет", typeName)
	return fields
}

// Р27: форма адресата из поля recipient попадает в описание шаблона
// (feed.TemplateDesc.Recipient), адресат SendX — поле To. Близнецы — subject и
// address; отличие одно — значение поля.
func TestNTF3_150_GeneratedDescCarriesRecipientForm(t *testing.T) {
	for form, ident := range map[string]string{"subject": "feed.RecipientSubject", "address": "feed.RecipientAddress"} {
		t.Run(form, func(t *testing.T) {
			tr := x2Tree(t)
			tr.edit("svc/notifications/invite/notification.yaml", "ttl: 168h", "ttl: 168h\nrecipient: "+form)
			tr.generate()
			src := tr.read("svc/notifications_invite.gen.go")
			require.Contains(t, src, "Recipient: "+ident)
			require.Contains(t, attrFields(t, src, "InviteAttrs"), "To")
			buildAgainstFeed(t, tr)
		})
	}
}

const resourceEventNotification = `name: resource-event
class: notice
ttl: 72h
recipient: fanout
attributes:
  kind: {type: text, presence: required}
  resource_id: {type: text, presence: required}
  change: {type: text, presence: required}
  occurred_at: {type: timestamp, presence: required}
  name: {type: text, presence: optional}
subject:
  ru: "{{ kind }} {{ resource_id }}: {{ change }}"
  en: "{{ kind }} {{ resource_id }}: {{ change }}"
`

const resourceEventBody = `blocks:
  - p: "{{ kind }} {{ resource_id }} {{ change }} {{ occurred_at }}"
  - p: "{{ name }}"
    when: name
`

// Р3, Р27, З10 п.3: форма fanout — у SendX нет адресата (поля To нет, PutID
// получает пустой адресат), name — optional: незаданное значение не даёт
// ключа. Сборка против feed.
func TestNTF3_Z10_FanoutSendXHasNoRecipientAndOptionalName(t *testing.T) {
	tr := x2Tree(t)
	tr.write("svc/notifications/resource-event/notification.yaml", resourceEventNotification)
	tr.write("svc/notifications/resource-event/body.ru.yaml", resourceEventBody)
	tr.write("svc/notifications/resource-event/body.en.yaml", resourceEventBody)
	tr.generate()
	src := tr.read("svc/notifications_resource-event.gen.go")
	fields := attrFields(t, src, "ResourceEventAttrs")
	require.NotContains(t, fields, "To", "у fanout адресата нет")
	require.Contains(t, fields, "Name")
	require.Contains(t, src, "Recipient: feed.RecipientFanout")
	require.Contains(t, src, `feed.PutID(ctx, tx, resourceEventDesc, "", values)`)
	require.Contains(t, src, `{Name: "name", Kind: form.KindText, Presence: feed.PresenceOptional, Subject: false}`)
	require.True(t, strings.Contains(src, "form.Presence(form.KindText, a.Name)"), "name optional: ключ ставится только у заданного значения")
	buildAgainstFeed(t, tr)
}

// Р14: лимит scope: project — описание несёт feed.ScopeProject, SendX — поле
// Project и ключ окна Values.Project. Близнец — тот же шаблон с лимитом
// initiator (A01).
func TestNTF3_R14_GeneratedProjectWindowKey(t *testing.T) {
	tr := x2Tree(t)
	tr.edit("svc/notifications/invite/notification.yaml", "{scope: initiator, window: 1h, max: 20}", "{scope: project, window: 1h, max: 200}")
	tr.generate()
	src := tr.read("svc/notifications_invite.gen.go")
	require.Contains(t, src, "{Scope: feed.ScopeProject, WindowSeconds: 3600, Max: 200}")
	fields := attrFields(t, src, "InviteAttrs")
	require.Equal(t, "string", fields["Project"], "поле ключа окна «проект»")
	require.NotContains(t, fields, "Initiator", "лимита на инициатора у шаблона нет")
	require.Contains(t, src, "Project: a.Project")
	buildAgainstFeed(t, tr)
}
