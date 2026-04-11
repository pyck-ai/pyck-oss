package instructions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/Yamashou/gqlgenc/clientv2"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/pyck-ai/pyck/backend/common/datatype"
	"github.com/pyck-ai/pyck/backend/common/env"
	"github.com/pyck-ai/pyck/backend/common/std"
	inventoryapi "github.com/pyck-ai/pyck/backend/inventory/api"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"
	managementent "github.com/pyck-ai/pyck/backend/management/ent/gen" //nolint:importas // Both inventory and management ent packages are used; cannot both be named 'ent'
)

const (
	apiUrlEnvName = "PYCK_API"
)

var (
	dataTypeSchemasRegex = regexp.MustCompile(`(?i).*_schema.json$`)
	repositoriesRegex    = regexp.MustCompile(`(?i).*_repo.json$`)
)

const (
	envNameFlag                = "env"
	envPathFlag                = "env-path"
	dataTypesFolderFlagName    = "data-types-folder"
	repositoriesFolderFlagName = "repositories-folder"
	locationsFolderFlagName    = "locations-folder"
	devicesFolderFlagName      = "devices-folder"
	authTokenFlagName          = "auth-token"
)

type jsonSchemaMetadata struct {
	Entity      string `json:"entity,omitempty"`
	Default     bool   `json:"default,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

type repositoryMetadata struct {
	Name         string                 `json:"name"`
	ParentName   string                 `json:"parentName"`
	VirtualRepo  bool                   `json:"virtualRepo"`
	Type         string                 `json:"type"`
	DataTypeName string                 `json:"dataTypeName"`
	Data         map[string]interface{} `json:"data"`
	Location     string                 `json:"location,omitempty"`
}

type locationMetadata struct {
	Name         string                 `json:"name"`
	DataTypeName string                 `json:"dataTypeName,omitempty"`
	Data         map[string]interface{} `json:"data"`
}

type deviceMetadata struct {
	Name         string                 `json:"name"`
	DataTypeName string                 `json:"dataTypeName,omitempty"`
	Data         map[string]interface{} `json:"data"`
	Location     string                 `json:"location,omitempty"`
}

func init() {
	migrateCmd.PersistentFlags().String(authTokenFlagName, "", "Auth token of Graphql-API. [PYCK_AUTH]")
	_ = viper.BindPFlag("AUTH", migrateCmd.PersistentFlags().Lookup(authTokenFlagName))

	migrateCmd.PersistentFlags().String(envNameFlag, "local", "Environment, for example dev")
	_ = viper.BindPFlag(envNameFlag, migrateCmd.PersistentFlags().Lookup(envNameFlag))

	migrateCmd.PersistentFlags().String(envPathFlag, "environments.yaml", "Environment file path, default environments.yaml")
	_ = viper.BindPFlag(envPathFlag, migrateCmd.PersistentFlags().Lookup(envPathFlag))

	migrateDataTypesCmd.PersistentFlags().String(dataTypesFolderFlagName, "config/dataTypes", "Data types folder")
	_ = viper.BindPFlag(dataTypesFolderFlagName, migrateDataTypesCmd.PersistentFlags().Lookup(dataTypesFolderFlagName))

	migrateRepositoriesCmd.PersistentFlags().String(repositoriesFolderFlagName, "config/repositories", "Repositories folder")
	_ = viper.BindPFlag(repositoriesFolderFlagName, migrateRepositoriesCmd.PersistentFlags().Lookup(repositoriesFolderFlagName))

	migrateLocationsCmd.PersistentFlags().String(locationsFolderFlagName, "config/locations", "Locations folder")
	_ = viper.BindPFlag(locationsFolderFlagName, migrateLocationsCmd.PersistentFlags().Lookup(locationsFolderFlagName))

	migrateDevicesCmd.PersistentFlags().String(devicesFolderFlagName, "config/devices", "Devices folder")
	_ = viper.BindPFlag(devicesFolderFlagName, migrateDevicesCmd.PersistentFlags().Lookup(devicesFolderFlagName))

	migrateCmd.AddCommand(migrateDataTypesCmd)
	migrateCmd.AddCommand(migrateRepositoriesCmd)
	migrateCmd.AddCommand(migrateLocationsCmd)
	migrateCmd.AddCommand(migrateDevicesCmd)
	rootCmd.AddCommand(migrateCmd)
}

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrate.",
	Long:  `Migrate.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("migrate")
	},
}

var migrateDataTypesCmd = &cobra.Command{
	Use:   "dataTypes",
	Short: "Add or overwrite data types.",
	Long:  `Add or overwrite data types. It will search recursively in config/dataTypes/* folders for _schema.json files.`,
	Run: func(cmd *cobra.Command, args []string) {
		authToken := viper.GetString("AUTH")
		commandToken, _ := cmd.Flags().GetString(authTokenFlagName)
		if len(commandToken) > 0 {
			authToken = commandToken
		}

		if authToken == "" {
			FailWithError(fmt.Errorf("--%s [PYCK_AUTH] is missing.", authTokenFlagName))
		}
		envName, _ := cmd.Flags().GetString(envNameFlag)
		envPath, _ := cmd.Flags().GetString(envPathFlag)
		dataTypesRootFolder, _ := cmd.Flags().GetString(dataTypesFolderFlagName)

		envs, err := env.ReadYamlEnv(envPath, envName)
		if err != nil {
			FailWithError(err)
		}

		apiUrl, ok := envs[apiUrlEnvName]
		if !ok {
			FailWithError(fmt.Errorf("api url not found in environment: %s", envName))
		}
		managementCli := newManagementClient(apiUrl, authToken)

		existingDataTypes, _ := getExistingDataTypesIds(managementCli)

		filePaths, err := getDataTypesFilePaths(dataTypesRootFolder)
		if err != nil {
			FailWithError(err)
		}

		ctx := context.Background()
		for _, filePath := range filePaths {
			err := upsertDataType(managementCli, ctx, filePath, existingDataTypes)
			if err != nil {
				FailWithError(err)
			}
		}
	},
}

var migrateRepositoriesCmd = &cobra.Command{
	Use:   "repositories",
	Short: "Add or overwrite repositories.",
	Long:  `Add or overwrite repositories. It will search recursively in config/repositories/* folders for _repo.json files.`,
	Run: func(cmd *cobra.Command, args []string) {
		authToken := viper.GetString("AUTH")
		commandToken, _ := cmd.Flags().GetString(authTokenFlagName)
		if len(commandToken) > 0 {
			authToken = commandToken
		}

		if authToken == "" {
			FailWithError(fmt.Errorf("--%s [PYCK_AUTH] is missing.", authTokenFlagName))
		}
		envName, _ := cmd.Flags().GetString(envNameFlag)
		envPath, _ := cmd.Flags().GetString(envPathFlag)
		repositoriesRootFolder, _ := cmd.Flags().GetString(repositoriesFolderFlagName)

		envs, err := env.ReadYamlEnv(envPath, envName)
		if err != nil {
			FailWithError(err)
		}

		apiUrl, ok := envs[apiUrlEnvName]
		if !ok {
			FailWithError(fmt.Errorf("api url not found in environment: %s", envName))
		}
		managementCli := newManagementClient(apiUrl, authToken)
		inventoryCli := newInventoryClient(apiUrl, authToken)

		repositoriesFolderGraph, err := parseRepositoriesGraph(repositoriesRootFolder)
		if err != nil {
			FailWithError(err)
		}

		locationsMap, err := getAllLocationsMap(managementCli)
		if err != nil {
			FailWithError(err)
		}

		err = upsertRepositories(managementCli, inventoryCli, repositoriesFolderGraph, locationsMap)
		if err != nil {
			FailWithError(err)
		}
	},
}

var migrateLocationsCmd = &cobra.Command{
	Use:   "locations",
	Short: "Add or overwrite locations.",
	Long:  `Add or overwrite locations. It will search for .json files in config/locations folder.`,
	Run: func(cmd *cobra.Command, args []string) {
		authToken := viper.GetString("AUTH")
		commandToken, _ := cmd.Flags().GetString(authTokenFlagName)
		if len(commandToken) > 0 {
			authToken = commandToken
		}

		if authToken == "" {
			FailWithError(fmt.Errorf("--%s [PYCK_AUTH] is missing.", authTokenFlagName))
		}
		envName, _ := cmd.Flags().GetString(envNameFlag)
		envPath, _ := cmd.Flags().GetString(envPathFlag)
		locationsRootFolder, _ := cmd.Flags().GetString(locationsFolderFlagName)

		envs, err := env.ReadYamlEnv(envPath, envName)
		if err != nil {
			FailWithError(err)
		}

		apiUrl, ok := envs[apiUrlEnvName]
		if !ok {
			FailWithError(fmt.Errorf("api url not found in environment: %s", envName))
		}

		managementCli := newManagementClient(apiUrl, authToken)

		existingLocations, err := getAllLocationsMap(managementCli)
		if err != nil {
			FailWithError(err)
		}

		filePaths, err := getLocationFilePaths(locationsRootFolder)
		if err != nil {
			FailWithError(err)
		}

		ctx := context.Background()
		for _, filePath := range filePaths {
			err := upsertLocation(managementCli, ctx, filePath, existingLocations)
			if err != nil {
				FailWithError(err)
			}
		}
	},
}

var migrateDevicesCmd = &cobra.Command{
	Use:   "devices",
	Short: "Add or overwrite devices.",
	Long:  `Add or overwrite devices. It will search for .json files in config/devices folder.`,
	Run: func(cmd *cobra.Command, args []string) {
		authToken := viper.GetString("AUTH")
		commandToken, _ := cmd.Flags().GetString(authTokenFlagName)
		if len(commandToken) > 0 {
			authToken = commandToken
		}

		if authToken == "" {
			FailWithError(fmt.Errorf("--%s [PYCK_AUTH] is missing.", authTokenFlagName))
		}
		envName, _ := cmd.Flags().GetString(envNameFlag)
		envPath, _ := cmd.Flags().GetString(envPathFlag)
		devicesRootFolder, _ := cmd.Flags().GetString(devicesFolderFlagName)

		envs, err := env.ReadYamlEnv(envPath, envName)
		if err != nil {
			FailWithError(err)
		}

		apiUrl, ok := envs[apiUrlEnvName]
		if !ok {
			FailWithError(fmt.Errorf("api url not found in environment: %s", envName))
		}

		managementCli := newManagementClient(apiUrl, authToken)

		existingDevices, err := getAllDevicesMap(managementCli)
		if err != nil {
			FailWithError(err)
		}

		existingLocations, err := getAllLocationsMap(managementCli)
		if err != nil {
			FailWithError(err)
		}

		filePaths, err := getDeviceFilePaths(devicesRootFolder)
		if err != nil {
			FailWithError(err)
		}

		ctx := context.Background()
		for _, filePath := range filePaths {
			err := upsertDevice(managementCli, ctx, filePath, existingDevices, existingLocations)
			if err != nil {
				FailWithError(err)
			}
		}
	},
}

func getExistingDataTypesIds(client managementapi.Client) (map[string]map[string]uuid.UUID, error) {
	ctx := context.Background()
	dataTypesIds := make(map[string]map[string]uuid.UUID)

	first := 100
	var after *string

	for {
		resp, err := client.GetDataTypes(ctx, managementapi.GetDataTypesArgs{
			After: after,
			First: &first,
		})
		if err != nil {
			return nil, err
		}

		data := resp.GetDataTypes()
		for _, edge := range data.Edges {
			if edge.Node != nil {
				entity := edge.Node.Entity
				if _, ok := dataTypesIds[entity]; !ok {
					dataTypesIds[entity] = make(map[string]uuid.UUID)
				}
				entityID, err := uuid.Parse(edge.Node.ID)
				if err != nil {
					return nil, fmt.Errorf("failed to parse data type ID %s: %w", edge.Node.ID, err)
				}
				dataTypesIds[entity][edge.Node.Name] = entityID
			}
		}

		if !data.PageInfo.HasNextPage {
			break
		}
		if data.PageInfo.EndCursor != nil {
			after = data.PageInfo.EndCursor
		}
	}

	return dataTypesIds, nil
}

func upsertDataType(client managementapi.Client, ctx context.Context, filePath string, existingDataTypes map[string]map[string]uuid.UUID) error {
	jsonSchemaBytes, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read file %w: %s", err, filePath)
	}

	schemaMeta, err := std.UnmarshalJson[jsonSchemaMetadata](jsonSchemaBytes)
	if err != nil {
		return fmt.Errorf("failed to unmarshal json schema: %w: [%s]", err, filePath)
	}

	entity := schemaMeta.Entity
	isDefault := schemaMeta.Default
	if !slices.Contains(datatype.DataTypeEntities(), entity) {
		fmt.Printf("WARNING: File %s has invalid entity: %s\n", filePath, entity)
		return nil
	}

	jsonSchema, err := std.UnmarshalJson[map[string]interface{}](jsonSchemaBytes)
	if err != nil {
		return fmt.Errorf("failed to unmarshal json schema: %w: [%s]", err, filePath)
	}
	delete(jsonSchema, "entity")
	delete(jsonSchema, "default")

	jsonSchemaBytes, err = std.MarshalJson(jsonSchema)
	if err != nil {
		return fmt.Errorf("failed to marshal json schema: %w: [%s]", err, filePath)
	}

	jsonSchemaStr := string(jsonSchemaBytes)

	dataTypeId, ok := existingDataTypes[entity][schemaMeta.Title]
	if ok {
		_, err := client.UpdateDataType(ctx, managementapi.UpdateDataTypeArgs{
			Id: dataTypeId.String(),
			Input: managementapi.UpdateDataTypeInput{
				Name:        &schemaMeta.Title,
				Description: &schemaMeta.Description,
				Default:     &isDefault,
				JSONSchema:  &jsonSchemaStr,
			},
		})
		if err != nil {
			return err
		}
	} else {
		_, err = client.CreateDataType(ctx, managementapi.CreateDataTypeArgs{
			Input: managementapi.CreateDataTypeInput{
				Name:        &schemaMeta.Title,
				Description: &schemaMeta.Description,
				Default:     &isDefault,
				Entity:      entity,
				JSONSchema:  jsonSchemaStr,
			},
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func getDataTypesFilePaths(folder string) ([]string, error) {
	files := []string{}
	err := filepath.Walk(folder, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && dataTypeSchemasRegex.MatchString(info.Name()) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk directory %s: %w", folder, err)
	}

	return files, nil
}

func parseRepositoriesGraph(rootDir string) (map[string][]string, error) {
	filesGraph := make(map[string][]string)

	err := filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() && path != rootDir {
			parentDir := filepath.Dir(path)

			if parentDir != path {
				filesGraph[parentDir] = append(filesGraph[parentDir], path)
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return filesGraph, nil
}

func upsertRepositories(managementCli managementapi.Client, inventoryCli inventoryapi.Client, repositoriesGraph map[string][]string, locationsMap map[string]*managementapi.GetLocations_Locations_Edges_Node) error {
	ctx := context.Background()
	repositoriesMap := make(map[string]*inventoryapi.GetRepositories_Repositories_Edges_Node)

	first := 100
	var after *string

	for {
		resp, err := inventoryCli.GetRepositories(ctx, inventoryapi.GetRepositoriesArgs{
			After: after,
			First: &first,
		})
		if err != nil {
			return err
		}

		data := resp.GetRepositories()
		for _, edge := range data.Edges {
			if edge.Node != nil {
				repositoriesMap[edge.Node.ID] = edge.Node
			}
		}

		if !data.PageInfo.HasNextPage {
			break
		}
		if data.PageInfo.EndCursor != nil {
			after = data.PageInfo.EndCursor
		}
	}

	allDataTypes, err := getExistingDataTypesIds(managementCli)
	if err != nil {
		return err
	}

	for parent, children := range repositoriesGraph {
		err := processRepoFolder(inventoryCli, parent, repositoriesMap, allDataTypes["repository"], locationsMap)
		if err != nil {
			return err
		}

		for _, child := range children {
			err := processRepoFolder(inventoryCli, child, repositoriesMap, allDataTypes["repository"], locationsMap)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func processRepoFolder(inventoryCli inventoryapi.Client, folder string, repositoriesMap map[string]*inventoryapi.GetRepositories_Repositories_Edges_Node, dataTypes map[string]uuid.UUID, locationsMap map[string]*managementapi.GetLocations_Locations_Edges_Node) error {
	ctx := context.Background()
	repoSources, err := getReposInFolder(folder)
	if err != nil {
		return err
	}

	repositoriesNameToID := make(map[string]string)
	for _, repo := range repositoriesMap {
		repositoriesNameToID[repo.Name] = repo.ID
	}

	for _, repoSource := range repoSources {
		repo, err := readFile[*repositoryMetadata](repoSource)
		if err != nil {
			return fmt.Errorf("failed to read repo: %s", repoSource)
		}

		if repo.Name == "" {
			continue
		}

		var locationID *uuid.UUID
		if repo.Location != "" {
			location, ok := locationsMap[repo.Location]
			if ok {
				if id, err := uuid.Parse(location.ID); err != nil {
					return fmt.Errorf("failed to parse location ID %s: %w", location.ID, err)
				} else {
					locationID = &id
				}
			}
		}

		dataTypeID, ok := dataTypes[repo.DataTypeName]
		if !ok {
			dataTypeID = uuid.UUID{}
		}
		var dataTypeIDPtr *uuid.UUID
		if dataTypeID != uuid.Nil {
			dataTypeIDPtr = &dataTypeID
		}

		repoType := entrepository.Type(repo.Type)

		existingRepoID, exists := repositoriesNameToID[repo.Name]

		if exists {
			name := repo.Name
			updateResp, err := inventoryCli.UpdateInventoryRepository(ctx, inventoryapi.UpdateInventoryRepositoryArgs{
				Id: existingRepoID,
				Input: inventoryapi.UpdateRepositoryInput{
					DataTypeID: dataTypeIDPtr,
					Data:       repo.Data,
					LocationID: locationID,
					Name:       &name,
					Type:       &repoType,
				},
			})
			if err != nil {
				return err
			}
			// Just update the name mapping - the repository already exists in repositoriesMap
			result := updateResp.GetUpdateInventoryRepository().GetInventoryRepository()
			repositoriesNameToID[repo.Name] = result.ID
		} else {
			virtualRepo := repo.VirtualRepo
			createResp, err := inventoryCli.CreateInventoryRepository(ctx, inventoryapi.CreateInventoryRepositoryArgs{
				Input: inventoryapi.CreateRepositoryInput{
					DataTypeID:  dataTypeIDPtr,
					Data:        repo.Data,
					LocationID:  locationID,
					Name:        repo.Name,
					Type:        repoType,
					VirtualRepo: &virtualRepo,
				},
			})
			if err != nil {
				return err
			}
			// For new repositories, we need to add them to both maps
			// Note: mutation response types differ from query types, but have same fields
			result := createResp.GetCreateInventoryRepository().GetInventoryRepository()
			repositoriesNameToID[repo.Name] = result.ID
			// Create a compatible entry for repositoriesMap using same field values
			repositoriesMap[result.ID] = &inventoryapi.GetRepositories_Repositories_Edges_Node{
				ID:          result.ID,
				Name:        result.Name,
				ParentID:    result.ParentID,
				VirtualRepo: result.VirtualRepo,
				Type:        result.Type,
			}
		}
	}
	return nil
}

func getReposInFolder(folderPath string) ([]string, error) {
	files := []string{}

	entries, err := os.ReadDir(folderPath)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() && repositoriesRegex.MatchString(entry.Name()) {
			files = append(files, filepath.Join(folderPath, entry.Name()))
		}
	}
	return files, nil
}

func readFile[T any](filePath string) (T, error) {
	var result T
	fileBytes, err := os.ReadFile(filePath)
	if err != nil {
		return result, err
	}

	err = json.Unmarshal(fileBytes, &result)
	if err != nil {
		return result, err
	}
	return result, nil
}

type authInterceptor struct {
	token string
}

func (a *authInterceptor) intercept(ctx context.Context, req *http.Request, gqlInfo *clientv2.GQLRequestInfo, res any, next clientv2.RequestInterceptorFunc) error {
	req.Header.Set("Authorization", "Bearer "+a.token)
	return next(ctx, req, gqlInfo, res)
}

//nolint:ireturn // Returning interface is intentional for dependency injection
func newManagementClient(apiUrl, authToken string) managementapi.Client {
	interceptor := &authInterceptor{token: authToken}
	return managementapi.NewClient(http.DefaultClient, apiUrl, nil, interceptor.intercept)
}

//nolint:ireturn // Returning interface is intentional for dependency injection
func newInventoryClient(apiUrl, authToken string) inventoryapi.Client {
	interceptor := &authInterceptor{token: authToken}
	return inventoryapi.NewClient(http.DefaultClient, apiUrl, nil, interceptor.intercept)
}

func getAllLocationsMap(client managementapi.Client) (map[string]*managementapi.GetLocations_Locations_Edges_Node, error) {
	ctx := context.Background()
	result := make(map[string]*managementapi.GetLocations_Locations_Edges_Node)

	first := 100
	var after *string

	for {
		resp, err := client.GetLocations(ctx, managementapi.GetLocationsArgs{
			After: after,
			First: &first,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to get locations: %w", err)
		}

		data := resp.GetLocations()
		for _, edge := range data.Edges {
			if edge.Node != nil {
				result[edge.Node.Name] = edge.Node
			}
		}

		if !data.PageInfo.HasNextPage {
			break
		}
		if data.PageInfo.EndCursor != nil {
			after = data.PageInfo.EndCursor
		}
	}

	return result, nil
}

func getLocationFilePaths(folder string) ([]string, error) {
	var files []string

	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil, fmt.Errorf("failed to read locations folder %s: %w", folder, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			files = append(files, filepath.Join(folder, entry.Name()))
		}
	}

	return files, nil
}

func upsertLocation(client managementapi.Client, ctx context.Context, filePath string, existingLocations map[string]*managementapi.GetLocations_Locations_Edges_Node) error {
	loc, err := readFile[*locationMetadata](filePath)
	if err != nil {
		return fmt.Errorf("failed to read location file %s: %w", filePath, err)
	}

	if loc.Name == "" {
		return nil
	}

	_, exists := existingLocations[loc.Name]
	if exists {
		return nil
	}

	input := managementent.CreateLocationInput{
		Name: loc.Name,
		Data: loc.Data,
	}

	if loc.DataTypeName != "" {
		input.DataTypeSlug = &loc.DataTypeName
	}

	deviceLocationIDs := make([]string, len(input.DeviceLocationsLocationIDs))
	for i, loc := range input.DeviceLocationsLocationIDs {
		deviceLocationIDs[i] = loc.String()
	}

	resp, err := client.CreateLocation(ctx, managementapi.CreateLocationArgs{
		Input: managementapi.CreateLocationInput{
			DataTypeID:                 input.DataTypeID,
			DataTypeSlug:               input.DataTypeSlug,
			Data:                       input.Data,
			Name:                       input.Name,
			DevicelocationslocationIDs: deviceLocationIDs,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create location '%s': %w", loc.Name, err)
	}

	// Mutation response type differs from query type, create compatible entry
	result := resp.GetCreateLocation().GetLocation()
	existingLocations[loc.Name] = &managementapi.GetLocations_Locations_Edges_Node{
		ID:   result.ID,
		Name: result.Name,
		Data: result.Data,
	}

	return nil
}

func getAllDevicesMap(client managementapi.Client) (map[string]*managementapi.GetDevices_Devices_Edges_Node, error) {
	ctx := context.Background()
	result := make(map[string]*managementapi.GetDevices_Devices_Edges_Node)

	first := 100
	var after *string

	for {
		resp, err := client.GetDevices(ctx, managementapi.GetDevicesArgs{
			After: after,
			First: &first,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to get devices: %w", err)
		}

		data := resp.GetDevices()
		for _, edge := range data.Edges {
			if edge.Node != nil {
				result[edge.Node.Name] = edge.Node
			}
		}

		if !data.PageInfo.HasNextPage {
			break
		}
		if data.PageInfo.EndCursor != nil {
			after = data.PageInfo.EndCursor
		}
	}

	return result, nil
}

func getDeviceFilePaths(folder string) ([]string, error) {
	var files []string

	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil, fmt.Errorf("failed to read devices folder %s: %w", folder, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			files = append(files, filepath.Join(folder, entry.Name()))
		}
	}

	return files, nil
}

func upsertDevice(client managementapi.Client, ctx context.Context, filePath string, existingDevices map[string]*managementapi.GetDevices_Devices_Edges_Node, existingLocations map[string]*managementapi.GetLocations_Locations_Edges_Node) error {
	dev, err := readFile[*deviceMetadata](filePath)
	if err != nil {
		return fmt.Errorf("failed to read device file %s: %w", filePath, err)
	}

	if dev.Name == "" {
		return nil
	}

	var deviceID uuid.UUID
	existingDevice, exists := existingDevices[dev.Name]
	if exists {
		id, err := uuid.Parse(existingDevice.ID)
		if err != nil {
			return fmt.Errorf("failed to parse existing device ID %s: %w", existingDevice.ID, err)
		}
		deviceID = id
	} else {
		input := managementent.CreateDeviceInput{
			Name: dev.Name,
			Data: dev.Data,
		}

		if dev.DataTypeName != "" {
			input.DataTypeSlug = &dev.DataTypeName
		}

		deviceLocationsDeviceIDs := make([]string, len(input.DeviceLocationsDeviceIDs))
		for i, loc := range input.DeviceLocationsDeviceIDs {
			deviceLocationsDeviceIDs[i] = loc.String()
		}

		deviceUsersDeviceIDs := make([]string, len(input.DeviceUsersDeviceIDs))
		for i, user := range input.DeviceUsersDeviceIDs {
			deviceUsersDeviceIDs[i] = user.String()
		}

		resp, err := client.CreateDevice(ctx, managementapi.CreateDeviceArgs{
			Input: managementapi.CreateDeviceInput{
				DataTypeID:               input.DataTypeID,
				DataTypeSlug:             input.DataTypeSlug,
				Data:                     input.Data,
				Name:                     input.Name,
				DevicelocationsdeviceIDs: deviceLocationsDeviceIDs,
				DeviceusersdeviceIDs:     deviceUsersDeviceIDs,
			},
		})
		if err != nil {
			return fmt.Errorf("failed to create device '%s': %w", dev.Name, err)
		}

		// Mutation response type differs from query type, create compatible entry
		result := resp.GetCreateDevice().GetDevice()
		existingDevices[dev.Name] = &managementapi.GetDevices_Devices_Edges_Node{
			ID:   result.ID,
			Name: result.Name,
			Data: result.Data,
		}
		id, err := uuid.Parse(result.ID)
		if err != nil {
			return fmt.Errorf("failed to parse created device ID %s: %w", result.ID, err)
		}

		deviceID = id
	}

	if dev.Location != "" {
		location, ok := existingLocations[dev.Location]
		if !ok {
			return fmt.Errorf("location '%s' not found for device '%s'", dev.Location, dev.Name)
		}
		locationID, err := uuid.Parse(location.ID)
		if err != nil {
			return fmt.Errorf("failed to parse location ID %s: %w", location.ID, err)
		}
		_, err = client.SetDeviceLocation(ctx, managementapi.SetDeviceLocationArgs{
			Input: managementapi.CreateDeviceLocationInput{
				DeviceID:   deviceID.String(),
				LocationID: locationID.String(),
			},
		})
		if err != nil {
			return fmt.Errorf("failed to set device location for '%s': %w", dev.Name, err)
		}
	}

	return nil
}
