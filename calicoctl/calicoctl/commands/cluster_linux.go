// Copyright (c) 2016 Tigera, Inc. All rights reserved.

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

package commands

import (
	"fmt"
	"strings"

	"github.com/docopt/docopt-go"
    "cluster"
	"github.com/projectcalico/calico/calicoctl/calicoctl/commands/constants"
	"github.com/projectcalico/calico/calicoctl/calicoctl/util"
)

// Cluster function is a switch to Cluster related sub-commands
func Cluster(args []string) error {
	var err error
	doc := constants.DatastoreIntro + `Usage:
  <BINARY_NAME> cluster <command> [<args>...]

    diags        Gather a diagnostics bundle for a Calico cluster.

Options:
  -h --help      Show this screen.

Description:
  Cluster specific commands for <BINARY_NAME>.  These commands must be run directly on
  the compute host running the Calico cluster instance.

  See '<BINARY_NAME> cluster <command> --help' to read about a specific subcommand.
`
	// Replace all instances of BINARY_NAME with the name of the binary.
	name, _ := util.NameAndDescription()
	doc = strings.ReplaceAll(doc, "<BINARY_NAME>", name)

	var parser = &docopt.Parser{
		HelpHandler:   docopt.PrintHelpAndExit,
		OptionsFirst:  true,
		SkipHelpFlags: false,
	}
	arguments, err := parser.ParseArgs(doc, args, "")
	if err != nil {
		return fmt.Errorf("Invalid option: 'calicoctl %s'. Use flag '--help' to read about a specific subcommand.", strings.Join(args, " "))
	}
	if arguments["<command>"] == nil {
		return nil
	}

	command := arguments["<command>"].(string)
	args = append([]string{"cluster", command}, arguments["<args>"].([]string)...)

	switch command {
	case "diags":
		return cluster.Diags(args)
	default:
		fmt.Println(doc)
	}

	return nil
}
