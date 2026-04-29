package instructions

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"slices"

	"github.com/Yamashou/gqlgenc/clientv2"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	inventoryapi "github.com/pyck-ai/pyck/backend/inventory/api"
	maindataapi "github.com/pyck-ai/pyck/backend/main-data/api"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"
	pickingapi "github.com/pyck-ai/pyck/backend/picking/api"
	receivingapi "github.com/pyck-ai/pyck/backend/receiving/api"
)

var (
	errGatewayURLMissing = errors.New("--gateway-url [PYCK_GATEWAY_URL] is missing")
	errAuthTokenMissing  = errors.New("auth token is missing")
)

func init() {
	viper.SetEnvPrefix("PYCK")
	viper.AutomaticEnv()

	rootCmd.PersistentFlags().String("gateway-url", "http://localhost:4000", "Gateway-url of Graphql-API. [PYCK_GATEWAY_URL]")
	_ = viper.BindPFlag("GATEWAY_URL", rootCmd.PersistentFlags().Lookup("gateway-url"))
}

var rootCmd = &cobra.Command{
	Use:   "pyck",
	Short: "pyck is a cli tool for handling virtual storage facilities.",
	Long: `pyck is a command-line utility that enables users to create, manage,
	and interact with simulated virtual storage facilities. It streamlines
	storage infrastructure testing, planning, and optimization, allowing
	developers and IT professionals to evaluate various
	configurations and performance scenarios effortlessly.`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// Commands that don't need API clients
		ignoreAPIClientCommands := []string{"setup", "migrate", "project", "uuid", "import-type"}
		if cmd.Parent() != nil && slices.Contains(ignoreAPIClientCommands, cmd.Parent().Name()) {
			return
		}
		if slices.Contains(ignoreAPIClientCommands, cmd.Name()) {
			return
		}

		// Basic validation of gateway URL and token
		gatewayURL := viper.GetString("GATEWAY_URL")
		if gatewayURL == "" {
			fmt.Println("--gateway-url [PYCK_GATEWAY_URL] is missing.")
			os.Exit(1)
		}
		token := viper.GetString("AUTH")
		commandToken, _ := cmd.Flags().GetString(authTokenFlagName)
		if len(commandToken) > 0 {
			token = commandToken
		}

		if token == "" {
			FailWithError(fmt.Errorf("%w: --%s [PYCK_AUTH]", errAuthTokenMissing, authTokenFlagName))
			os.Exit(1)
		}
	},
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) == 0 {
			_ = cmd.Help()
			os.Exit(0)
		}
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func FailWithError(err error) {
	fmt.Printf("Error %s\n", err)
	os.Exit(1)
}

func getRandomEntry[T any](slice []T) T {
	lenSlice := len(slice)
	randomIndex := rand.Intn(lenSlice-0) + 0
	return slice[randomIndex]
}

type clientAuthInterceptor struct {
	token string
}

func (a *clientAuthInterceptor) intercept(ctx context.Context, req *http.Request, gqlInfo *clientv2.GQLRequestInfo, res any, next clientv2.RequestInterceptorFunc) error {
	req.Header.Set("Authorization", "Bearer "+a.token)
	return next(ctx, req, gqlInfo, res)
}

//nolint:ireturn // Returning interface is intentional for dependency injection
func getManagementClient(cmd *cobra.Command) (managementapi.Client, error) {
	gatewayURL := viper.GetString("GATEWAY_URL")
	token := viper.GetString("AUTH")
	commandToken, _ := cmd.Flags().GetString(authTokenFlagName)
	if len(commandToken) > 0 {
		token = commandToken
	}

	if gatewayURL == "" {
		return nil, errGatewayURLMissing
	}
	if token == "" {
		return nil, fmt.Errorf("%w: --%s [PYCK_AUTH]", errAuthTokenMissing, authTokenFlagName)
	}

	interceptor := &clientAuthInterceptor{token: token}
	return managementapi.NewClient(http.DefaultClient, gatewayURL, nil, interceptor.intercept), nil
}

//nolint:ireturn // Returning interface is intentional for dependency injection
func getInventoryClient(cmd *cobra.Command) (inventoryapi.Client, error) {
	gatewayURL := viper.GetString("GATEWAY_URL")
	token := viper.GetString("AUTH")
	commandToken, _ := cmd.Flags().GetString(authTokenFlagName)
	if len(commandToken) > 0 {
		token = commandToken
	}

	if gatewayURL == "" {
		return nil, errGatewayURLMissing
	}
	if token == "" {
		return nil, fmt.Errorf("%w: --%s [PYCK_AUTH]", errAuthTokenMissing, authTokenFlagName)
	}

	interceptor := &clientAuthInterceptor{token: token}
	return inventoryapi.NewClient(http.DefaultClient, gatewayURL, nil, interceptor.intercept), nil
}

//nolint:ireturn // Returning interface is intentional for dependency injection
func getMainDataClient(cmd *cobra.Command) (maindataapi.Client, error) {
	gatewayURL := viper.GetString("GATEWAY_URL")
	token := viper.GetString("AUTH")
	commandToken, _ := cmd.Flags().GetString(authTokenFlagName)
	if len(commandToken) > 0 {
		token = commandToken
	}

	if gatewayURL == "" {
		return nil, errGatewayURLMissing
	}
	if token == "" {
		return nil, fmt.Errorf("%w: --%s [PYCK_AUTH]", errAuthTokenMissing, authTokenFlagName)
	}

	interceptor := &clientAuthInterceptor{token: token}
	return maindataapi.NewClient(http.DefaultClient, gatewayURL, nil, interceptor.intercept), nil
}

//nolint:ireturn // Returning interface is intentional for dependency injection
func getPickingClient(cmd *cobra.Command) (pickingapi.Client, error) {
	gatewayURL := viper.GetString("GATEWAY_URL")
	token := viper.GetString("AUTH")
	commandToken, _ := cmd.Flags().GetString(authTokenFlagName)
	if len(commandToken) > 0 {
		token = commandToken
	}

	if gatewayURL == "" {
		return nil, errGatewayURLMissing
	}
	if token == "" {
		return nil, fmt.Errorf("%w: --%s [PYCK_AUTH]", errAuthTokenMissing, authTokenFlagName)
	}

	interceptor := &clientAuthInterceptor{token: token}
	return pickingapi.NewClient(http.DefaultClient, gatewayURL, nil, interceptor.intercept), nil
}

//nolint:ireturn // Returning interface is intentional for dependency injection
func getReceivingClient(cmd *cobra.Command) (receivingapi.Client, error) {
	gatewayURL := viper.GetString("GATEWAY_URL")
	token := viper.GetString("AUTH")
	commandToken, _ := cmd.Flags().GetString(authTokenFlagName)
	if len(commandToken) > 0 {
		token = commandToken
	}

	if gatewayURL == "" {
		return nil, errGatewayURLMissing
	}
	if token == "" {
		return nil, fmt.Errorf("%w: --%s [PYCK_AUTH]", errAuthTokenMissing, authTokenFlagName)
	}

	interceptor := &clientAuthInterceptor{token: token}
	return receivingapi.NewClient(http.DefaultClient, gatewayURL, nil, interceptor.intercept), nil
}
