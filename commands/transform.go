package commands

import (
	"archive/zip"
	"fmt"
	"os"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/mattermost/mmetl/services/slack"
)

var TransformCmd = &cobra.Command{
	Use:   "transform",
	Short: "Transforms export files into Mattermost import files",
}

var TransformSlackCmd = &cobra.Command{
	Use:     "slack",
	Short:   "Transforms a Slack export.",
	Long:    "Transforms a Slack export zipfile into a Mattermost export JSONL file.",
	Example: "  transform slack --team myteam --file my_export.zip --output mm_export.json",
	Args:    cobra.NoArgs,
	RunE:    transformSlackCmdF,
}

func init() {
	TransformSlackCmd.Flags().StringP("team", "t", "", "an existing team in Mattermost to import the data into")
	if err := TransformSlackCmd.MarkFlagRequired("team"); err != nil {
		panic(err)
	}
	TransformSlackCmd.Flags().StringP("file", "f", "", "the Slack export file to transform")
	if err := TransformSlackCmd.MarkFlagRequired("file"); err != nil {
		panic(err)
	}
	TransformSlackCmd.Flags().StringP("output", "o", "bulk-export.jsonl", "the output path")
	TransformSlackCmd.Flags().StringP("attachments-dir", "d", "bulk-export-attachments", "the path for the attachments directory")
	TransformSlackCmd.Flags().BoolP("skip-convert-posts", "c", false, "Skips converting mentions and post markup. Only for testing purposes")
	TransformSlackCmd.Flags().BoolP("skip-attachments", "a", false, "Skips copying the attachments from the import file")
	TransformSlackCmd.Flags().BoolP("discard-invalid-props", "p", false, "Skips converting posts with invalid props instead discarding the props themselves")
	TransformSlackCmd.Flags().Bool("debug", true, "Whether to show debug logs or not")
	TransformSlackCmd.Flags().Bool("auth-data-as-email", false, "Set auth data the same as user's email")
	TransformSlackCmd.Flags().StringP("auth-service", "s", "", "Set auth service value for SSO using")
	TransformSlackCmd.Flags().String("redis-endpoint", "", "redis endpoint")
	TransformSlackCmd.Flags().String("redis-login", "", "redis user")
	TransformSlackCmd.Flags().String("redis-password", "", "redis password")
	TransformSlackCmd.Flags().Bool("import-workflow-messages", false, "import workflow messages")
	TransformSlackCmd.Flags().Bool("skip-posts", false, "do not import posts")
	TransformSlackCmd.Flags().Bool("skip-channels", false, "do not import channels and posts")
	TransformCmd.AddCommand(
		TransformSlackCmd,
	)

	RootCmd.AddCommand(
		TransformCmd,
	)
}

func transformSlackCmdF(cmd *cobra.Command, args []string) error {
	team, _ := cmd.Flags().GetString("team")
	inputFilePath, _ := cmd.Flags().GetString("file")
	outputFilePath, _ := cmd.Flags().GetString("output")
	attachmentsDir, _ := cmd.Flags().GetString("attachments-dir")
	skipConvertPosts, _ := cmd.Flags().GetBool("skip-convert-posts")
	skipAttachments, _ := cmd.Flags().GetBool("skip-attachments")
	discardInvalidProps, _ := cmd.Flags().GetBool("discard-invalid-props")
	redisEndpoint, _ := cmd.Flags().GetString("redis-endpoint")
	redisLogin, _ := cmd.Flags().GetString("redis-login")
	redisPassword, _ := cmd.Flags().GetString("redis-password")
	debug, _ := cmd.Flags().GetBool("debug")
	setAuthDataAsEmail, _ := cmd.Flags().GetBool("auth-data-as-email")
	authService, _ := cmd.Flags().GetString("auth-service")
	importWorkflowMessages, _ := cmd.Flags().GetBool("import-workflow-messages")
	skipPosts, _ := cmd.Flags().GetBool("skip-posts")
	skipChannels, _ := cmd.Flags().GetBool("skip-channels")

	skipConvertPosts = skipConvertPosts || skipPosts

	// output file
	if fileInfo, err := os.Stat(outputFilePath); err != nil && !os.IsNotExist(err) {
		return err
	} else if err == nil && fileInfo.IsDir() {
		return fmt.Errorf("Output file \"%s\" is a directory", outputFilePath)
	}

	// attachments dir
	if !skipAttachments {
		if fileInfo, err := os.Stat(attachmentsDir); os.IsNotExist(err) {
			if createErr := os.Mkdir(attachmentsDir, 0755); createErr != nil {
				return createErr
			}
		} else if err != nil {
			return err
		} else if !fileInfo.IsDir() {
			return fmt.Errorf("File \"%s\" is not a directory", attachmentsDir)
		}
	}

	// input file
	fileReader, err := os.Open(inputFilePath)
	if err != nil {
		return err
	}
	defer fileReader.Close()

	zipFileInfo, err := fileReader.Stat()
	if err != nil {
		return err
	}

	zipReader, err := zip.NewReader(fileReader, zipFileInfo.Size())
	if err != nil || zipReader.File == nil {
		return err
	}

	logger := log.New()
	logger.Level = log.WarnLevel
	if debug {
		logger.Level = log.DebugLevel
	}
	slackTransformer := slack.NewTransformer(team, logger)

	slackExport, err := slackTransformer.ParseSlackExportFile(zipReader, skipConvertPosts)
	if err != nil {
		return err
	}

	var redisConfig *slack.RedisConfig
	if len(redisEndpoint) > 0 {
		redisConfig = &slack.RedisConfig{
			Addr:     redisEndpoint,
			User:     redisLogin,
			Password: redisPassword,
		}
	}
	err = slackTransformer.Transform(&slack.TransformConfig{
		AttachmentsDir:         attachmentsDir,
		SkipAttachments:        skipAttachments,
		DiscardInvalidProps:    discardInvalidProps,
		AuthDataAsEmail:        setAuthDataAsEmail,
		AuthService:            authService,
		ImportWorkflowMessages: importWorkflowMessages,
		SkipPosts:              skipPosts,
		SkipChannels:           skipChannels,
		RedisConfig:            redisConfig,
	}, slackExport)
	if err != nil {
		return err
	}

	if err = slackTransformer.Export(outputFilePath); err != nil {
		return err
	}

	slackTransformer.Logger.Info("Transformation succeeded!")

	return nil
}
