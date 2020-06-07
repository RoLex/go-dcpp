package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/spf13/cobra"

	"github.com/direct-connect/go-dcpp/hub"
)

func init() {
	cmdProf := &cobra.Command{
		Use:     "profiles [command]",
		Aliases: []string{"profile", "prof", "p"},
		Short:   "profile-related commands",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return OpenDB()
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			list, err := hubDB.ListProfiles()
			if err != nil {
				return err
			}
			fmt.Println("NAME\t\tPARENT\t\tPROFILE")
			var last error
			def := hub.DefaultProfiles()
			for _, name := range list {
				m, err := hubDB.GetProfile(name)
				if err != nil {
					fmt.Printf("%s\t\t-\t\tERR\n", name)
					log.Println("error:", err)
					last = err
					continue
				}
				if dm, ok := def[name]; ok {
					if m == nil {
						m = dm
					} else {
						for k, v := range dm {
							m[k] = v
						}
					}
				}
				par, _ := m[hub.ProfileParent].(string)
				delete(m, hub.ProfileParent)
				if par == "" {
					par = "-"
				}
				data, _ := json.Marshal(m)
				fmt.Printf("%s\t\t%s\t\t%s\n", name, par, string(data))
			}
			for name, m := range def {
				par, _ := m[hub.ProfileParent].(string)
				delete(m, hub.ProfileParent)
				if par == "" {
					par = "-"
				}
				data, _ := json.Marshal(m)
				fmt.Printf("%s\t\t%s\t\t%s\n", name, par, string(data))
			}
			return last
		},
		PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
			return CloseDB()
		},
	}
	Root.AddCommand(cmdProf)

	cmdList := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "list user profiles",
		RunE:    cmdProf.RunE,
	}
	cmdProf.AddCommand(cmdList)

	cmdAdd := &cobra.Command{
		Use:     "create <name> [parent]",
		Aliases: []string{"add"},
		Short:   "create a new user profile",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 || len(args) > 2 {
				return errors.New("expected 1 or 2 arguments")
			}
			name := args[0]
			if name == "" {
				return errors.New("name should not be empty")
			}
			def := hub.DefaultProfiles()
			m := make(hub.Map)
			if dm, ok := def[name]; ok {
				m = dm.Clone()
			}
			if len(args) > 1 && args[1] != "" {
				parent := args[1]
				if _, ok := def[parent]; !ok {
					m, err := hubDB.GetProfile(parent)
					if err != nil {
						return err
					} else if m == nil {
						return errors.New("parent profile does not exist")
					}
				}
				m[hub.ProfileParent] = parent
			}
			return hubDB.PutProfile(name, m)
		},
	}
	cmdProf.AddCommand(cmdAdd)

	cmdDel := &cobra.Command{
		Use:     "delete <name>",
		Aliases: []string{"del", "rm"},
		Short:   "delete profile",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return errors.New("expected profile name")
			}
			name := args[0]
			if name == "" {
				return errors.New("name should not be empty")
			}
			return hubDB.DelProfile(name)
		},
	}
	cmdProf.AddCommand(cmdDel)
}
