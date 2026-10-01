// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package main

import "runtime/debug"

const corelibModule = "github.com/PRO-Robotech/corelib"

// moduleVersion — версия модуля corelib, из которого собран генератор
// (NTF1-D06): у потребителя — версия пина, внутри corelib — (devel).
func moduleVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return corelibModule + " (unknown)"
	}
	if bi.Main.Path == corelibModule {
		return corelibModule + " " + bi.Main.Version
	}
	for _, d := range bi.Deps {
		if d.Path == corelibModule {
			if d.Replace != nil {
				return corelibModule + " " + d.Version + " => " + d.Replace.Path + " " + d.Replace.Version
			}
			return corelibModule + " " + d.Version
		}
	}
	return corelibModule + " " + bi.Main.Version
}
