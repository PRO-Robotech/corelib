// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package cobraargs_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/PRO-Robotech/corelib/migratorcli"
	"github.com/PRO-Robotech/corelib/migratorcli/cobraargs"
)

// TestNoExtraArgumentsSpeaksTheSharedText — лишний позиционный аргумент
// отвергается ТЕМ ЖЕ текстом, что и у прямой формы (#1461).
func TestNoExtraArgumentsSpeaksTheSharedText(t *testing.T) {
	cmd := &cobra.Command{Use: "up"}
	cmd.SetArgs(nil)
	err := cobraargs.NoExtraArguments(cmd, []string{"800001"})
	if err == nil {
		t.Fatal("лишний позиционный аргумент принят молча")
	}
	if want := migratorcli.UnexpectedArgumentError("up", "800001").Error(); err.Error() != want {
		t.Fatalf("своя редакция отказа:\n  получено: %q\n  общая:    %q", err.Error(), want)
	}
}

// TestNoExtraArgumentsIsSilentOnALegitimateCall — положительный контроль: без
// него проба выше зеленела бы на проверке, отвергающей вообще всё.
func TestNoExtraArgumentsIsSilentOnALegitimateCall(t *testing.T) {
	if err := cobraargs.NoExtraArguments(&cobra.Command{Use: "up"}, nil); err != nil {
		t.Fatalf("законный вызов без позиционных аргументов отвергнут: %v", err)
	}
}

// TestOnlyKnownCommandsSpeaksTheSharedText — на корне первый позиционный
// аргумент, не совпавший ни с одной подкомандой, есть неизвестная подкоманда, и
// отказ обязан назвать перечень известных.
func TestOnlyKnownCommandsSpeaksTheSharedText(t *testing.T) {
	err := cobraargs.OnlyKnownCommands(&cobra.Command{Use: "kacho-migrator"}, []string{"upp"})
	if err == nil {
		t.Fatal("неизвестная подкоманда принята молча")
	}
	if want := migratorcli.UnknownCommandError("kacho-migrator", "upp").Error(); err.Error() != want {
		t.Fatalf("своя редакция отказа:\n  получено: %q\n  общая:    %q", err.Error(), want)
	}
}

func TestOnlyKnownCommandsIsSilentOnAnEmptyTail(t *testing.T) {
	if err := cobraargs.OnlyKnownCommands(&cobra.Command{Use: "kacho-migrator"}, nil); err != nil {
		t.Fatalf("пустой хвост объявлен неизвестной подкомандой: %v", err)
	}
}

// TestHideShellCompletionLeavesTheCommandsSevenShare — перечень команд один на
// семь. `completion` снимается, `help` остаётся и понимается прямой формой —
// равенство достигнуто с той стороны, где оно достижимо без чужого имени.
func TestHideShellCompletionLeavesTheCommandsSevenShare(t *testing.T) {
	// Подкоманды несут исполнение: без него cobra относит их не к перечню
	// команд, а к «дополнительным темам помощи», и проба утверждала бы о другом
	// блоке вывода, чем тот, который читает оператор.
	run := func(cmd *cobra.Command, args []string) error { return nil }
	root := &cobra.Command{Use: "kacho-migrator"}
	root.AddCommand(&cobra.Command{Use: "up", Short: "apply", RunE: run, Args: cobraargs.NoExtraArguments})
	root.AddCommand(&cobra.Command{Use: "down", Short: "rollback", RunE: run, Args: cobraargs.NoExtraArguments})
	root.AddCommand(&cobra.Command{Use: "status", Short: "show", RunE: run, Args: cobraargs.NoExtraArguments})
	cobraargs.HideShellCompletion(root)

	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("помощь объявлена отказом: %v", err)
	}

	block := out.String()
	i := strings.Index(block, "Available Commands:")
	if i < 0 {
		t.Fatalf("в помощи нет блока перечня команд: %q", out.String())
	}
	block = block[i:]
	if j := strings.Index(block, "\nFlags:"); j >= 0 {
		block = block[:j]
	}
	for _, want := range []string{"up", "down", "status"} {
		if !strings.Contains(block, want) {
			t.Errorf("в перечне нет %q: %q", want, block)
		}
	}
	if strings.Contains(block, "completion") {
		t.Errorf("в перечне есть completion, которого нет у прямой формы: %q", block)
	}
	// `help` остаётся намеренно: прямая форма его понимает, и снятие потребовало
	// бы регистрации скрытой команды под чужим именем.
	if !strings.Contains(block, "help") {
		t.Errorf("help пропал из перечня, хотя равенство по нему достигнуто иначе: %q", block)
	}
}
