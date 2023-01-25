// Copyright (c) 2020-2022 Tigera, Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package xdp

import (
	"fmt"
	"path"
	"strings"

	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"

	"github.com/projectcalico/calico/felix/bpf"
	"github.com/projectcalico/calico/felix/bpf/bpfdefs"
	"github.com/projectcalico/calico/felix/bpf/hook"
	"github.com/projectcalico/calico/felix/bpf/libbpf"
	tcdefs "github.com/projectcalico/calico/felix/bpf/tc/defs"
)

var JumpMapIndexes = map[string]map[int]string{
	"IPv4": map[int]string{
		tcdefs.ProgIndexMain:    "calico_xdp_main",
		tcdefs.ProgIndexPolicy:  "calico_xdp_norm_pol_tail",
		tcdefs.ProgIndexAllowed: "calico_xdp_accepted_entrypoint",
		tcdefs.ProgIndexDrop:    "calico_xdp_drop",
	},
}

const DetachedID = 0

type AttachPoint struct {
	bpf.AttachPoint

	Modes []bpf.XDPMode
}

func (ap *AttachPoint) PolicyAllowJumpIdx(family int) int {
	return tcdefs.ProgIndexAllowed
}

func (ap *AttachPoint) PolicyDenyJumpIdx(family int) int {
	return tcdefs.ProgIndexDrop
}

func (ap *AttachPoint) Config() string {
	return fmt.Sprintf("%+v", ap)
}

func (ap *AttachPoint) FileName() string {
	logLevel := strings.ToLower(ap.LogLevel)
	if logLevel == "off" {
		logLevel = "no_log"
	}
	return "xdp_" + logLevel + ".o"
}

func (ap *AttachPoint) ProgramName() string {
	return "cali_xdp_preamble"
}

func (ap *AttachPoint) Log() *log.Entry {
	return log.WithFields(log.Fields{
		"iface":    ap.Iface,
		"modes":    ap.Modes,
		"logLevel": ap.LogLevel,
	})
}

func (ap *AttachPoint) AlreadyAttached(object string) (int, bool) {
	progID, err := ap.ProgramID()
	if err != nil {
		ap.Log().Debugf("Couldn't get the attached XDP program ID. err=%v", err)
		return -1, false
	}

	somethingAttached, err := ap.IsAttached()
	if err != nil {
		ap.Log().Debugf("Failed to verify if any program is attached to interface. err=%v", err)
		return -1, false
	}

	isAttached, err := bpf.AlreadyAttachedProg(ap, object, progID)
	if err != nil {
		ap.Log().Debugf("Failed to check if BPF program was already attached. err=%v", err)
		return -1, false
	}

	if isAttached && somethingAttached {
		return progID, true
	}
	return -1, false
}

func ConfigureProgram(m *libbpf.Map, iface string, globalData *libbpf.XDPGlobalData) error {
	in := []byte("---------------")
	copy(in, iface)
	globalData.IfaceName = string(in)

	if err := libbpf.XDPSetGlobals(m, globalData); err != nil {
		return fmt.Errorf("failed to configure xdp: %w", err)
	}

	return nil
}

func (ap *AttachPoint) AttachProgram() (int, error) {
	// By now the attach type specific generic set of programs is loaded and we
	// only need to load and configure the preamble that will pass the
	// configuration further to the selected set of programs.
	binaryToLoad := path.Join(bpfdefs.ObjectDir, "xdp_preamble.o")

	obj, err := libbpf.OpenObject(binaryToLoad)
	if err != nil {
		return -1, err
	}
	defer obj.Close()

	for m, err := obj.FirstMap(); m != nil && err == nil; m, err = m.NextMap() {
		if m.IsMapInternal() {
			var globals libbpf.XDPGlobalData

			// XXX We have a single type of a program so far, change if we
			// introduce more. Leaving it like this so far for simplicity.
			for i := 0; i < tcdefs.ProgIndexEnd; i++ {
				globals.Jumps[i] = uint32(i)
			}
			globals.Jumps[tcdefs.ProgIndexPolicy] = uint32(ap.PolicyIdx(4))

			if err := ConfigureProgram(m, ap.Iface, &globals); err != nil {
				return -1, err
			}
			continue
		}
		// TODO: We need to set map size here like tc.
		pinDir := bpf.MapPinDir(m.Type(), m.Name(), ap.Iface, hook.XDP)
		if err := m.SetPinPath(path.Join(pinDir, m.Name())); err != nil {
			return -1, fmt.Errorf("error pinning map %s: %w", m.Name(), err)
		}
	}

	// Check if the bpf object is already attached, and we should skip re-attaching it
	progID, isAttached := ap.AlreadyAttached(binaryToLoad)
	if isAttached {
		ap.Log().Infof("Programs already attached, skip reattaching %s", binaryToLoad)
		return progID, nil
	}
	ap.Log().Infof("Continue with attaching BPF program %s", binaryToLoad)

	if err := obj.Load(); err != nil {
		ap.Log().Warn("Failed to load program")
		return -1, fmt.Errorf("error loading program: %w", err)
	}

	oldID, err := ap.ProgramID()
	if err != nil {
		return -1, fmt.Errorf("failed to get the attached XDP program ID: %w", err)
	}

	attachmentSucceeded := false
	for _, mode := range ap.Modes {
		ap.Log().Debugf("Trying to attach XDP program in mode %v - old id: %v", mode, oldID)
		// Force attach the program. If there is already a program attached, the replacement only
		// succeed in the same mode of the current program.
		progID, err = obj.AttachXDP(ap.Iface, ap.ProgramName(), oldID, unix.XDP_FLAGS_REPLACE|uint(mode))
		if err != nil || progID == DetachedID || progID == oldID {
			ap.Log().WithError(err).Warnf("Failed to attach to XDP program %s mode %v", ap.ProgramName(), mode)
		} else {
			ap.Log().Debugf("Successfully attached XDP program in mode %v. ID: %v", mode, progID)
			attachmentSucceeded = true
			break
		}
	}

	if !attachmentSucceeded {
		return -1, fmt.Errorf("failed to attach XDP program with program name %v to interface %v",
			ap.ProgramName(), ap.Iface)
	}

	return progID, nil
}

func (ap *AttachPoint) DetachProgram() error {
	// Get the current XDP program ID, if any.
	progID, err := ap.ProgramID()
	if err != nil {
		return fmt.Errorf("failed to get the attached XDP program ID: %w", err)
	}
	if progID == DetachedID {
		ap.Log().Debugf("No XDP program attached.")
		return nil
	}

	ourProg, err := bpf.AlreadyAttachedProg(ap, path.Join(bpfdefs.ObjectDir, ap.FileName()), progID)
	if err != nil || !ourProg {
		return fmt.Errorf("XDP expected program ID does match with current one: %w", err)
	}

	// Try to remove our XDP program in all modes, until the program ID is 0
	removalSucceeded := false
	for _, mode := range ap.Modes {
		err = libbpf.DetachXDP(ap.Iface, uint(mode))
		ap.Log().Debugf("Trying to detach XDP program in mode %v.", mode)
		if err != nil {
			ap.Log().Debugf("Failed to detach XDP program in mode %v: %v.", mode, err)
			continue
		}
		curProgId, err := ap.ProgramID()
		if err != nil {
			return fmt.Errorf("failed to get the attached XDP program ID: %w", err)
		}

		if curProgId == DetachedID {
			removalSucceeded = true
			ap.Log().Debugf("Successfully detached XDP program.")
			break
		}
	}
	if !removalSucceeded {
		return fmt.Errorf("couldn't remove our XDP program. program ID: %v", progID)
	}

	ap.Log().Infof("XDP program detached. program ID: %v", progID)

	// Program is detached, now remove the json file we saved for it
	if err = bpf.ForgetAttachedProg(ap.IfaceName(), hook.XDP); err != nil {
		return fmt.Errorf("failed to delete hash of BPF program from disk: %w", err)
	}
	return nil
}

func (ap *AttachPoint) IsAttached() (bool, error) {
	_, err := ap.ProgramID()
	return err == nil, err
}

func (ap *AttachPoint) ProgramID() (int, error) {
	progID, err := libbpf.GetXDPProgramID(ap.Iface)
	if err != nil {
		return -1, fmt.Errorf("Couldn't check for XDP program on iface %v: %w", ap.Iface, err)
	}
	return progID, nil
}

func UpdateJumpMap(obj *libbpf.Obj, progs map[int]string) error {
	for idx, name := range progs {
		err := obj.UpdateJumpMap(hook.NewXDPProgramsMap().GetName(), name, idx)
		if err != nil {
			return fmt.Errorf("failed to update program '%s' at index %d: %w", name, idx, err)
		}
		log.Debugf("xdp set program '%s' at index %d", name, idx)
	}

	return nil
}
