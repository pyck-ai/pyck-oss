package instructions

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/pyck-ai/pyck/backend/common/services/kubernetes"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel"
	"github.com/spf13/cobra"
)

type jsonKeyFile struct {
	ClientId string `json:"client_id"`
}

type setupResult struct {
	ServiceToken       string `json:"service_token,omitempty"`
	ServiceWorkerToken string `json:"service_worker_token,omitempty"`
	APIUserToken       string `json:"api_user_token,omitempty"`
	OrgID              string `json:"org_id,omitempty"`
	ProjectID          string `json:"project_id,omitempty"`
	Audience           string `json:"audience,omitempty"`
	KeyFileJSON        string `json:"key_file_json,omitempty"`
	AdminEmail         string `json:"admin_email,omitempty"`
	AdminPassword      string `json:"admin_password,omitempty"`
}

func init() {
	zitadelCmd.Flags().String("issuer", "", "Issuer, for example 'https://auth.dev.pyck.cloud:8080'")
	_ = zitadelCmd.MarkFlagRequired("issuer")
	zitadelCmd.Flags().String("jwt-profile-path", "", "Path to JWT-Key-File generated at startup of Zitadel")
	_ = zitadelCmd.MarkFlagRequired("jwt-profile-path")
	zitadelCmd.Flags().String("admin-email", "", "Email address of the zitadel admin user")
	_ = zitadelCmd.MarkFlagRequired("admin-email")

	zitadelTenantCmd.Flags().String("issuer", "", "Issuer, for example 'https://auth.dev.pyck.cloud:8080'")
	_ = zitadelTenantCmd.MarkFlagRequired("issuer")
	zitadelTenantCmd.Flags().String("jwt-profile-path", "", "Path to JWT-Key-File generated at startup of Zitadel")
	_ = zitadelTenantCmd.MarkFlagRequired("jwt-profile-path")
	zitadelTenantCmd.Flags().String("name", "", "Name of the organization")
	_ = zitadelTenantCmd.MarkFlagRequired("name")
	zitadelTenantCmd.Flags().String("project-id", "", "Project ID to grant permissions for")
	_ = zitadelTenantCmd.MarkFlagRequired("project-id")

	setupCmd.PersistentFlags().Bool("create-k8s-secret", false, "Write secrets to k8s secret store")
	setupCmd.PersistentFlags().Bool("k8s-in-cluster", true, "Command runs in-cluster, default 'true'")
	setupCmd.PersistentFlags().String("k8s-namespace", "", "Kubernetes namespace")
	setupCmd.PersistentFlags().String("k8s-config-path", "", "Kubernetes config absolute path, default $HOME/.kube/config")

	oidcDebugCmd.Flags().String("issuer", "", "Issuer, for example 'https://auth.dev.pyck.cloud:8080'")
	_ = oidcDebugCmd.MarkFlagRequired("issuer")
	oidcDebugCmd.Flags().String("jwt-profile-path", "", "Path to JWT-Key-File generated at startup of Zitadel")
	_ = oidcDebugCmd.MarkFlagRequired("jwt-profile-path")
	oidcDebugCmd.Flags().String("project-id", "", "Project ID where to add the OIDC app")
	_ = oidcDebugCmd.MarkFlagRequired("project-id")
	oidcDebugCmd.Flags().String("name", "JWT Debug Frontend", "Application name")
	oidcDebugCmd.Flags().String("port", "4182", "Port for the debug frontend")

	zitadelSyncTriggerCmd.Flags().String("issuer", "", "Zitadel issuer URL")
	_ = zitadelSyncTriggerCmd.MarkFlagRequired("issuer")
	zitadelSyncTriggerCmd.Flags().String("jwt-profile-path", "", "Path to JWT key file")
	_ = zitadelSyncTriggerCmd.MarkFlagRequired("jwt-profile-path")
	zitadelSyncTriggerCmd.Flags().String("webhook-url", "", "Webhook URL to call when metadata changes")
	_ = zitadelSyncTriggerCmd.MarkFlagRequired("webhook-url")
	zitadelSyncTriggerCmd.Flags().Bool("tls-secure", false, "Enable TLS certificate verification (default: false for local dev)")

	setupCmd.AddCommand(zitadelTenantCmd)
	setupCmd.AddCommand(zitadelCmd)
	setupCmd.AddCommand(oidcDebugCmd)
	setupCmd.AddCommand(zitadelSyncTriggerCmd)
	rootCmd.AddCommand(setupCmd)
}

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Setup stack.",
	Long:  `Setup stack.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("setup")
	},
}

var zitadelCmd = &cobra.Command{
	Use:   "zitadel",
	Short: "Setup Zitadel",
	Run: func(cmd *cobra.Command, args []string) {
		issuer, _ := cmd.Flags().GetString("issuer")
		api := strings.Replace(issuer, "https://", "", 1)
		jwtProfilePath, _ := cmd.Flags().GetString("jwt-profile-path")
		createK8sSecrets, _ := cmd.Flags().GetBool("create-k8s-secret")
		k8sNamespace, _ := cmd.Flags().GetString("k8s-namespace")
		k8sInCluster, _ := cmd.Flags().GetBool("k8s-in-cluster")
		k8sConfigPath, _ := cmd.Flags().GetString("k8s-config-path")
		adminEmail, _ := cmd.Flags().GetString("admin-email")
		adminPassword := "Password1!"

		// result for output
		setupResult := setupResult{
			Audience:      issuer,
			AdminEmail:    adminEmail,
			AdminPassword: adminPassword,
		}
		ctx := context.Background()

		zitadelClient, err := zitadel.SdkClient(ctx, issuer, api, jwtProfilePath, "")
		if err != nil {
			log.Fatalln("could not create client", err)
		}
		defer func() {
			zitadelClient.Close()
		}()

		// get org info
		orgID, err := zitadelClient.GetOrgID(ctx)
		if err != nil {
			FailWithError(err)
		}
		setupResult.OrgID = orgID

		// create human user
		user, err := zitadelClient.CreateHumanUser(ctx, "administrator", "Pyck", "Administrator", adminEmail, true, adminPassword, false)
		if err != nil {
			FailWithError(err)
		}

		// make user to IAM admin
		err = zitadelClient.AddIAMMember(ctx, user.ID, []string{"IAM_OWNER"})
		if err != nil {
			FailWithError(err)
		}

		// create api project
		project, err := zitadelClient.AddProject(ctx, "Pyck")
		if err != nil {
			FailWithError(err)
		}
		setupResult.ProjectID = project.ID

		// create Roles
		projectRoles := []string{zitadel.ProjectRoleSystem, zitadel.ProjectRoleAdmin, zitadel.ProjectRoleWriter, zitadel.ProjectRoleReader, zitadel.ProjectRoleTemporalReader, zitadel.ProjectRoleTemporalWriter, zitadel.ProjectRoleTemporalAdmin}
		err = zitadelClient.AddProjectRoles(ctx, project.ID, projectRoles)
		if err != nil {
			FailWithError(err)
		}

		// add api app to project
		app, err := zitadelClient.AddApiAppToProject(ctx, project.ID, "Pyck-API")
		if err != nil {
			FailWithError(err)
		}

		// create and download app key
		appKey, err := zitadelClient.AddJsonAppKey(ctx, project.ID, app.ID)
		if err != nil {
			FailWithError(err)
		}
		setupResult.KeyFileJSON = appKey.JSON

		var jsonFile jsonKeyFile
		err = json.Unmarshal([]byte(appKey.JSON), &jsonFile)
		if err != nil {
			FailWithError(err)
		}

		// add service user for root org
		serviceUser, err := zitadelClient.AddServiceUser(ctx, "service-user", "Service User")
		if err != nil {
			FailWithError(err)
		}

		// Add PAT token for service user
		serviceUserToken, err := zitadelClient.AddServiceUserToken(ctx, serviceUser.ID)
		if err != nil {
			FailWithError(err)
		}
		setupResult.ServiceToken = serviceUserToken.Token

		// add service user grant
		err = zitadelClient.AddUserGrantForProject(ctx, project.ID, serviceUser.ID, []string{"system"})
		if err != nil {
			FailWithError(err)
		}

		// write k8s secret or print json
		if createK8sSecrets {
			if k8sNamespace == "" {
				log.Fatalln("Kubernetes namespace must be specified for creating secrets.")
			}

			k8sClient, err := kubernetes.NewK8sClient(k8sNamespace, k8sInCluster, k8sConfigPath)
			if err != nil {
				FailWithError(err)
			}
			secretData := map[string][]byte{
				"pyck-api-app-id":         []byte(app.ID),
				"pyck-api-client-id":      []byte(jsonFile.ClientId),
				"pyck-api-keyfile.json":   []byte(setupResult.KeyFileJSON),
				"pyck-service-token":      []byte(setupResult.ServiceToken),
				"pyck-zitadel-org-id":     []byte(setupResult.OrgID),
				"pyck-zitadel-project-id": []byte(setupResult.ProjectID),
				"pyck-zitadel-audience":   []byte(setupResult.Audience),
			}
			err = k8sClient.CreateSecrets(ctx, "pyck-secrets", secretData)
			if err != nil {
				FailWithError(err)
			}
		}

		// print out result json
		resultJSON, _ := json.Marshal(setupResult)
		fmt.Println(string(resultJSON))
	},
}

var zitadelTenantCmd = &cobra.Command{
	Use:   "tenant",
	Short: "Setup tenant.",
	Long:  `Setup Zitadel tenant.`,
	Run: func(cmd *cobra.Command, args []string) {
		issuer, _ := cmd.Flags().GetString("issuer")
		api := strings.Replace(issuer, "https://", "", 1)
		jwtProfilePath, _ := cmd.Flags().GetString("jwt-profile-path")
		projectID, _ := cmd.Flags().GetString("project-id")
		name, _ := cmd.Flags().GetString("name")
		createK8sSecrets, _ := cmd.Flags().GetBool("create-k8s-secret")
		k8sNamespace, _ := cmd.Flags().GetString("k8s-namespace")
		k8sInCluster, _ := cmd.Flags().GetBool("k8s-in-cluster")
		k8sConfigPath, _ := cmd.Flags().GetString("k8s-config-path")

		// result for output
		setupResult := setupResult{
			Audience: issuer,
		}

		ctx := context.Background()
		_ = ctx
		_ = setupResult
		_ = projectID

		zitadelClient, err := zitadel.SdkClient(ctx, issuer, api, jwtProfilePath, "")
		if err != nil {
			log.Fatalln("could not create client", err)
		}
		defer func() {
			zitadelClient.Close()
		}()

		// add org
		org, err := zitadelClient.AddOrganization(ctx, name)
		if err != nil {
			FailWithError(err)
		}
		setupResult.OrgID = org.ID

		// add grant for project to org
		grant, err := zitadelClient.AddProjectGrant(ctx, projectID, org.ID, []string{"writer", "reader"})
		if err != nil {
			FailWithError(err)
		}
		_ = grant

		zitadelTenantClient, err := zitadel.SdkClient(ctx, issuer, api, jwtProfilePath, org.ID)
		if err != nil {
			log.Fatalln("could not create tenant client", err)
		}
		defer func() {
			zitadelClient.Close()
			zitadelTenantClient.Close()
		}()

		// create tenant service user
		serviceUser, err := zitadelTenantClient.AddServiceUser(ctx,
			fmt.Sprintf("%s-%s", org.ID, "service-worker-user"), "Service Worker User")
		if err != nil {
			FailWithError(err)
		}

		// Add PAT token for service user
		serviceUserToken, err := zitadelTenantClient.AddServiceUserToken(ctx, serviceUser.ID)
		if err != nil {
			FailWithError(err)
		}
		setupResult.ServiceWorkerToken = serviceUserToken.Token

		// add service user grant
		err = zitadelTenantClient.AddUserGrant(ctx, projectID, serviceUser.ID, grant.ID, []string{"writer"})
		if err != nil {
			FailWithError(err)
		}

		// create tenant api user
		apiUser, err := zitadelTenantClient.AddServiceUser(ctx, fmt.Sprintf("%s-%s", org.ID, "api-user"), "API User")
		if err != nil {
			FailWithError(err)
		}

		// Add PAT token for api user
		apiUserToken, err := zitadelTenantClient.AddServiceUserToken(ctx, serviceUser.ID)
		if err != nil {
			FailWithError(err)
		}
		setupResult.APIUserToken = apiUserToken.Token

		// add service api grant
		err = zitadelTenantClient.AddUserGrant(ctx, projectID, apiUser.ID, grant.ID, []string{"writer"})
		if err != nil {
			FailWithError(err)
		}

		// write k8s secret or print json
		if createK8sSecrets {
			if k8sNamespace == "" {
				log.Fatalln("Kubernetes namespace must be specified for creating secrets.")
			}

			k8sClient, err := kubernetes.NewK8sClient(k8sNamespace, k8sInCluster, k8sConfigPath)
			if err != nil {
				FailWithError(err)
			}
			secretData := map[string][]byte{
				"pyck-tenant-service-token": []byte(setupResult.ServiceWorkerToken),
			}
			err = k8sClient.CreateSecrets(ctx, "pyck-tenant-secrets", secretData)
			if err != nil {
				FailWithError(err)
			}
			fmt.Println("k8s secret 'pyck-tenant-secrets' created.")
		} else {
			// print out result json
			resultJSON, _ := json.Marshal(setupResult)
			fmt.Println(string(resultJSON))
		}
	},
}

var oidcDebugCmd = &cobra.Command{
	Use:   "oidc-debug",
	Short: "Setup OIDC Debug Frontend Application",
	Long:  `Creates an OIDC application in Zitadel for the debug frontend that provides JWT tokens for development.`,
	Run: func(cmd *cobra.Command, args []string) {
		issuer, _ := cmd.Flags().GetString("issuer")
		api := strings.Replace(issuer, "https://", "", 1)
		jwtProfilePath, _ := cmd.Flags().GetString("jwt-profile-path")
		projectID, _ := cmd.Flags().GetString("project-id")
		appName, _ := cmd.Flags().GetString("name")
		port, _ := cmd.Flags().GetString("port")

		ctx := context.Background()

		zitadelClient, err := zitadel.SdkClient(ctx, issuer, api, jwtProfilePath, "")
		if err != nil {
			log.Fatalln("could not create client", err)
		}
		defer func() {
			zitadelClient.Close()
		}()

		// Create OIDC Debug Frontend App
		redirectURI := fmt.Sprintf("http://localhost:%s/callback", port)
		redirectURIs := []string{redirectURI, fmt.Sprintf("http://localhost:%s", port)}
		postLogoutRedirectURIs := []string{fmt.Sprintf("http://localhost:%s", port)}

		app, err := zitadelClient.AddOIDCAppToProject(ctx, projectID, appName, redirectURIs, postLogoutRedirectURIs)
		if err != nil {
			FailWithError(err)
		}

		// Output result as JSON
		result := map[string]string{
			"client_id":     app.ClientID,
			"client_secret": app.ClientSecret,
			"issuer":        issuer,
			"redirect_uri":  redirectURI,
		}

		resultJSON, err := json.Marshal(result)
		if err != nil {
			log.Fatalln("could not marshal result to JSON", err)
		}

		fmt.Println(string(resultJSON))
	},
}

var zitadelSyncTriggerCmd = &cobra.Command{
	Use:   "zitadel-sync-trigger",
	Short: "Setup Zitadel trigger for tenant sync webhook",
	Long: `Configures Zitadel to call the management service webhook when organization metadata changes.
This enables automatic tenant synchronization when metadata (like flavour) is updated in Zitadel.`,
	Run: func(cmd *cobra.Command, args []string) {
		issuer, _ := cmd.Flags().GetString("issuer")
		jwtProfilePath, _ := cmd.Flags().GetString("jwt-profile-path")
		webhookUrl, _ := cmd.Flags().GetString("webhook-url")
		tlsSecure, _ := cmd.Flags().GetBool("tls-secure")

		zitadelClient := zitadel.HttpClient(issuer, jwtProfilePath, tlsSecure)

		// Enable Actions feature (required for targets/executions)
		err := zitadelClient.UpdateActionFeature(zitadel.FeatureLevelInstance, true)
		if err != nil {
			FailWithError(fmt.Errorf("failed to enable Actions feature: %w", err))
		}

		// Create or update target for sync webhook
		target, err := zitadelClient.CreateOrUpdateActionTarget(
			"pyck-tenant-sync",
			false, // interruptOnError
			webhookUrl,
			10*time.Second,
		)
		if err != nil {
			FailWithError(err)
		}

		// Bind to SetOrgMetadata gRPC method
		err = zitadelClient.CreateExecution(target.ID, "/zitadel.management.v1.ManagementService/SetOrgMetadata", false)
		if err != nil {
			FailWithError(err)
		}

		// Output result as JSON
		result := map[string]string{
			"target_id":   target.ID,
			"webhook_url": webhookUrl,
		}

		resultJSON, err := json.Marshal(result)
		if err != nil {
			log.Fatalln("could not marshal result to JSON", err)
		}

		fmt.Println(string(resultJSON))
	},
}
