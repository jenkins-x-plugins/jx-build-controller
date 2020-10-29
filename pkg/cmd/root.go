package cmd

import (
	"github.com/jenkins-x-plugins/jx-build-controller/pkg/cmd/controller"
	"github.com/spf13/cobra"
)

// Main creates a command object for the command
func Main() (*cobra.Command, *controller.ControllerOptions) {
	return controller.NewCmdController()
}
