package slack

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/mattermost/mattermost-server/v6/model"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"golang.org/x/text/unicode/norm"
)

type IntermediateChannel struct {
	Id               string            `json:"id"`
	OriginalName     string            `json:"original_name"`
	Name             string            `json:"name"`
	DisplayName      string            `json:"display_name"`
	Members          []string          `json:"members"`
	MembersUsernames []string          `json:"members_usernames"`
	Purpose          string            `json:"purpose"`
	Header           string            `json:"header"`
	Topic            string            `json:"topic"`
	Type             model.ChannelType `json:"type"`
}

const WorkflowUserName = "imported-workflow"

// PosgreSQLMaxPostSize is the default PostgreSQL max post size in mattermost 6.6
const PosgreSQLMaxPostSize = 65535 / 4

func (c *IntermediateChannel) Sanitise(logger log.FieldLogger) {
	if c.Type == model.ChannelTypeDirect {
		return
	}

	c.Name = strings.Trim(c.Name, "_-")
	if len(c.Name) > model.ChannelNameMaxLength {
		logger.Warnf("Channel %s handle exceeds the maximum length. It will be truncated when imported.", c.DisplayName)
		c.Name = c.Name[0:model.ChannelNameMaxLength]
	}
	if len(c.Name) == 1 {
		c.Name = "slack-channel-" + c.Name
	}
	if !isValidChannelNameCharacters(c.Name) {
		c.Name = strings.ToLower(c.Id)
	}

	c.DisplayName = strings.Trim(c.DisplayName, "_-")
	if utf8.RuneCountInString(c.DisplayName) > model.ChannelDisplayNameMaxRunes {
		logger.Warnf("Channel %s display name exceeds the maximum length. It will be truncated when imported.", c.DisplayName)
		c.DisplayName = truncateRunes(c.DisplayName, model.ChannelDisplayNameMaxRunes)
	}
	if len(c.DisplayName) == 1 {
		c.DisplayName = "slack-channel-" + c.DisplayName
	}

	if utf8.RuneCountInString(c.Purpose) > model.ChannelPurposeMaxRunes {
		logger.Warnf("Channel %s purpose exceeds the maximum length. It will be truncated when imported.", c.DisplayName)
		c.Purpose = truncateRunes(c.Purpose, model.ChannelPurposeMaxRunes)
	}

	if utf8.RuneCountInString(c.Header) > model.ChannelHeaderMaxRunes {
		logger.Warnf("Channel %s header exceeds the maximum length. It will be truncated when imported.", c.DisplayName)
		c.Header = truncateRunes(c.Header, model.ChannelHeaderMaxRunes)
	}
}

type IntermediateUser struct {
	Id          string   `json:"id"`
	Username    string   `json:"username"`
	FirstName   string   `json:"first_name"`
	LastName    string   `json:"last_name"`
	Position    string   `json:"position"`
	Email       string   `json:"email"`
	Password    string   `json:"password"`
	Memberships []string `json:"memberships"`
	AuthData    *string  `json:"auth_data"`
	AuthService string   `json:"auth_service"`
}

func (u *IntermediateUser) Sanitise(logger log.FieldLogger) {
	if u.Email == "" {
		u.Email = u.Username + "@example.com"
		logger.Warnf("User %s does not have an email address in the Slack export. Used %s as a placeholder. The user should update their email address once logged in to the system.", u.Username, u.Email)
	}

	if utf8.RuneCountInString(u.Position) > model.UserPositionMaxRunes {
		logger.Warnf("User %s position %s is too long. Field will be truncated", u.Username, u.Position)
		u.Position = u.Position[0:model.UserPositionMaxRunes]
	}

	if utf8.RuneCountInString(u.FirstName) > model.UserFirstNameMaxRunes {
		logger.Warnf("User %s first name %s is too long. Field will be truncated", u.Username, u.FirstName)
		u.FirstName = u.FirstName[0:model.UserFirstNameMaxRunes]
	}

	if utf8.RuneCountInString(u.LastName) > model.UserLastNameMaxRunes {
		logger.Warnf("User %s last name %s is too long. Field will be truncated", u.Username, u.LastName)
		u.LastName = u.LastName[0:model.UserLastNameMaxRunes]
	}
}

type IntermediatePost struct {
	User     string                `json:"user"`
	Channel  string                `json:"channel"`
	Message  string                `json:"message"`
	Props    model.StringInterface `json:"props"`
	CreateAt int64                 `json:"create_at"`
	// Type           string              `json:"type"`
	Attachments    []string            `json:"attachments"`
	Replies        []*IntermediatePost `json:"replies"`
	IsDirect       bool                `json:"is_direct"`
	ChannelMembers []string            `json:"channel_members"`
}

func (s *IntermediatePost) Sanitise() {
	if utf8.RuneCountInString(s.Message) > PosgreSQLMaxPostSize {
		s.Message = string([]rune(s.Message)[:PosgreSQLMaxPostSize])
	}
}

type Intermediate struct {
	PublicChannels  []*IntermediateChannel       `json:"public_channels"`
	PrivateChannels []*IntermediateChannel       `json:"private_channels"`
	GroupChannels   []*IntermediateChannel       `json:"group_channels"`
	DirectChannels  []*IntermediateChannel       `json:"direct_channels"`
	UsersById       map[string]*IntermediateUser `json:"users"`
	Posts           []*IntermediatePost          `json:"posts"`
}

func (t *Transformer) TransformUsers(users []SlackUser, authDataAsEmail bool, authService string) {
	t.Logger.Info("Transforming users")

	resultUsers := map[string]*IntermediateUser{}
	for _, user := range users {
		newUser := &IntermediateUser{
			Id:        user.Id,
			Username:  user.Username,
			FirstName: user.Profile.FirstName,
			LastName:  user.Profile.LastName,
			Position:  user.Profile.Title,
			Email:     user.Profile.Email,
		}

		newUser.Sanitise(t.Logger)

		if authDataAsEmail && authService != "" {
			newUser.AuthData = &newUser.Email
			newUser.AuthService = authService
		}

		resultUsers[newUser.Id] = newUser
		t.Logger.Debugf("Slack user with email %s has been imported.", newUser.Email)
	}

	t.Intermediate.UsersById = resultUsers
}

func filterValidMembers(members []string, users map[string]*IntermediateUser) []string {
	validMembers := []string{}
	for _, member := range members {
		if _, ok := users[member]; ok {
			validMembers = append(validMembers, member)
		}
	}

	return validMembers
}

func getOriginalName(channel SlackChannel) string {
	if channel.Name == "" {
		return channel.Id
	} else {
		return channel.Name
	}
}

func (t *Transformer) TransformChannels(channels []SlackChannel) []*IntermediateChannel {
	resultChannels := []*IntermediateChannel{}
	for _, channel := range channels {
		validMembers := filterValidMembers(channel.Members, t.Intermediate.UsersById)
		if (channel.Type == model.ChannelTypeDirect || channel.Type == model.ChannelTypeGroup) && len(validMembers) <= 1 {
			t.Logger.Warnf("Bulk export for direct channels containing a single member is not supported. Not importing channel %s", channel.Name)
			continue
		}

		if channel.Type == model.ChannelTypeGroup && len(validMembers) > model.ChannelGroupMaxUsers {
			channel.Name = channel.Purpose.Value
			channel.Type = model.ChannelTypePrivate
		}

		name := SlackConvertChannelName(channel.Name, channel.Id)
		newChannel := &IntermediateChannel{
			OriginalName: getOriginalName(channel),
			Name:         name,
			DisplayName:  getOriginalName(channel),
			Members:      validMembers,
			Purpose:      channel.Purpose.Value,
			Header:       channel.Topic.Value,
			Type:         channel.Type,
		}

		newChannel.Sanitise(t.Logger)
		resultChannels = append(resultChannels, newChannel)
	}

	return resultChannels
}

func (t *Transformer) PopulateUserMemberships() {
	t.Logger.Info("Populating user memberships")

	for userId, user := range t.Intermediate.UsersById {
		memberships := []string{}
		for _, channel := range t.Intermediate.PublicChannels {
			for _, memberId := range channel.Members {
				if userId == memberId {
					memberships = append(memberships, channel.Name)
					break
				}
			}
		}
		for _, channel := range t.Intermediate.PrivateChannels {
			for _, memberId := range channel.Members {
				if userId == memberId {
					memberships = append(memberships, channel.Name)
					break
				}
			}
		}
		user.Memberships = memberships
	}
}

func (t *Transformer) PopulateChannelMemberships() {
	t.Logger.Info("Populating channel memberships")

	for _, channel := range t.Intermediate.GroupChannels {
		members := []string{}
		for _, memberId := range channel.Members {
			if user, ok := t.Intermediate.UsersById[memberId]; ok {
				members = append(members, user.Username)
			}
		}

		channel.MembersUsernames = members
	}
	for _, channel := range t.Intermediate.DirectChannels {
		members := []string{}
		for _, memberId := range channel.Members {
			if user, ok := t.Intermediate.UsersById[memberId]; ok {
				members = append(members, user.Username)
			}
		}

		channel.MembersUsernames = members
	}
}

func (t *Transformer) TransformAllChannels(slackExport *SlackExport) error {
	t.Logger.Info("Transforming channels")

	// transform public
	t.Intermediate.PublicChannels = t.TransformChannels(slackExport.PublicChannels)

	// transform private
	t.Intermediate.PrivateChannels = t.TransformChannels(slackExport.PrivateChannels)

	// transform group
	regularGroupChannels, bigGroupChannels := SplitChannelsByMemberSize(slackExport.GroupChannels, model.ChannelGroupMaxUsers)

	t.Intermediate.PrivateChannels = append(t.Intermediate.PrivateChannels, t.TransformChannels(bigGroupChannels)...)

	t.Intermediate.GroupChannels = t.TransformChannels(regularGroupChannels)

	// transform direct
	t.Intermediate.DirectChannels = t.TransformChannels(slackExport.DirectChannels)

	return nil
}

func AddPostToThreads(original SlackPost, post *IntermediatePost, threads ThreadsStorage, channel *IntermediateChannel, timestamps map[int64]bool, importWorkflowPosts bool) {
	// direct and group posts need the channel members in the import line
	if channel.Type == model.ChannelTypeDirect || channel.Type == model.ChannelTypeGroup {
		post.IsDirect = true
		post.ChannelMembers = channel.MembersUsernames
	} else {
		post.IsDirect = false
	}

	// avoid timestamp duplications
	for {
		// if the timestamp hasn't been used already, break and use
		if _, ok := timestamps[post.CreateAt]; !ok {
			break
		}
		post.CreateAt++
	}
	timestamps[post.CreateAt] = true

	// if post is part of a thread
	if original.ThreadTS != "" && original.ThreadTS != original.TimeStamp {
		rootPost := threads.LookupThread(original.ThreadTS)
		if rootPost == nil {
			log.Printf("ERROR processing post in thread, couldn't find rootPost: %+v\n", original)
			return
		}
		if !importWorkflowPosts && rootPost.User == WorkflowUserName {
			return
		}
		rootPost.Replies = append(rootPost.Replies, post)
		return
	}

	post.Sanitise()
	// if post is the root of a thread
	if original.TimeStamp == original.ThreadTS {
		if threads.HasThread(original.ThreadTS) {
			log.Println("WARNING: overwriting root post for thread " + original.ThreadTS)
		}
		threads.StoreThread(original.ThreadTS, post)
		return
	}

	if threads.HasThread(original.TimeStamp) {
		log.Println("WARNING: overwriting root post for thread " + original.TimeStamp)
	}
	threads.StoreThread(original.TimeStamp, post)
}

func buildChannelsByOriginalNameMap(intermediate *Intermediate) map[string]*IntermediateChannel {
	channelsByName := map[string]*IntermediateChannel{}
	for _, channel := range intermediate.PublicChannels {
		channelsByName[channel.OriginalName] = channel
	}
	for _, channel := range intermediate.PrivateChannels {
		channelsByName[channel.OriginalName] = channel
	}
	for _, channel := range intermediate.GroupChannels {
		channelsByName[channel.OriginalName] = channel
	}
	for _, channel := range intermediate.DirectChannels {
		channelsByName[channel.OriginalName] = channel
	}
	return channelsByName
}

func getNormalisedFilePath(file *SlackFile, attachmentsDir string) string {
	filePath := path.Join(attachmentsDir, fmt.Sprintf("%s_%s", file.Id, file.Name))
	return string(norm.NFC.Bytes([]byte(filePath)))
}

func addFileToPost(file *SlackFile, uploads map[string]*zip.File, post *IntermediatePost, attachmentsDir string) error {
	zipFile, ok := uploads[file.Id]
	if !ok {
		return errors.Errorf("failed to retrieve file with id %s", file.Id)
	}

	zipFileReader, err := zipFile.Open()
	if err != nil {
		return errors.Wrapf(err, "failed to open attachment from zipfile for id %s", file.Id)
	}
	defer zipFileReader.Close()

	destFilePath := getNormalisedFilePath(file, attachmentsDir)
	destFile, err := os.Create(destFilePath)
	if err != nil {
		return errors.Wrapf(err, "failed to create file %s in the attachments directory", file.Id)
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, zipFileReader)
	if err != nil {
		return errors.Wrapf(err, "failed to create file %s in the attachments directory", file.Id)
	}

	log.Printf("SUCCESS COPYING FILE %s TO DEST %s", file.Id, destFilePath)

	post.Attachments = append(post.Attachments, destFilePath)

	return nil
}

func (t *Transformer) newChannelThreadsStorage(channelName, attachmentsDir string, redisConfig *RedisConfig) (ThreadsStorage, error) {
	if redisConfig == nil {
		return newMemoryStorage(), nil
	}
	if t.redisFactory == nil {
		factory, err := newRedisFactory(redisConfig)
		if err != nil {
			return nil, err
		}
		t.redisFactory = factory
	}
	return t.redisFactory.newRedisStorage(channelName, attachmentsDir), nil
}

func (t *Transformer) selectOrCreateWorkflowUser(post SlackPost) *IntermediateUser {
	userID := "importedworkflow"
	existingUser, ok := t.Intermediate.UsersById[userID]
	if ok {
		return existingUser
	}
	newUser := &IntermediateUser{
		Id:        userID,
		Username:  WorkflowUserName,
		FirstName: WorkflowUserName,
		LastName:  "",
		Email:     "imported-workflow@tinkoff.ru",
		Password:  model.NewId(),
	}

	newUser.Sanitise(t.Logger)
	t.Intermediate.UsersById[userID] = newUser
	return newUser
}

func (t *Transformer) TransformPosts(cfg *TransformConfig, slackExport *SlackExport) error {
	t.Logger.Info("Transforming posts")

	newGroupChannels := []*IntermediateChannel{}
	newDirectChannels := []*IntermediateChannel{}
	channelsByOriginalName := buildChannelsByOriginalNameMap(t.Intermediate)

	resultPosts := []*IntermediatePost{}
	for originalChannelName, channelPosts := range slackExport.Posts {
		channel, ok := channelsByOriginalName[originalChannelName]
		if !ok {
			t.Logger.Warnf("--- Couldn't find channel %s referenced by posts", originalChannelName)
			continue
		}

		timestamps := make(map[int64]bool)
		sort.Slice(channelPosts, func(i, j int) bool {
			return SlackConvertTimeStamp(channelPosts[i].TimeStamp) < SlackConvertTimeStamp(channelPosts[j].TimeStamp)
		})
		threads, err := t.newChannelThreadsStorage(originalChannelName, cfg.AttachmentsDir, cfg.RedisConfig)
		if err != nil {
			return err
		}

		for _, post := range channelPosts {
			switch {
			// plain message that can have files attached
			case post.IsPlainMessage():
				if post.User == "" {
					t.Logger.Warn("Unable to import the message as the user field is missing.")
					continue
				}
				author := t.Intermediate.UsersById[post.User]
				if author == nil {
					t.Logger.Warnf("Unable to add the message as the Slack user does not exist in Mattermost. user=%s", post.User)
					continue
				}
				newPost := &IntermediatePost{
					User:     author.Username,
					Channel:  channel.Name,
					Message:  post.Text,
					CreateAt: SlackConvertTimeStamp(post.TimeStamp),
				}
				if (post.File != nil || post.Files != nil) && !cfg.SkipAttachments {
					if post.File != nil {
						err := addFileToPost(post.File, slackExport.Uploads, newPost, cfg.AttachmentsDir)
						if err != nil {
							t.Logger.WithError(err).Error("Failed to add file to post")
						}
					} else if post.Files != nil {
						for _, file := range post.Files {
							err := addFileToPost(file, slackExport.Uploads, newPost, cfg.AttachmentsDir)
							if err != nil {
								t.Logger.WithError(err).Error("Failed to add file to post")
							}
						}
					}
				}

				if len(post.Attachments) > 0 {
					props := model.StringInterface{"attachments": post.Attachments}
					propsB, _ := json.Marshal(props)

					if utf8.RuneCountInString(string(propsB)) <= model.PostPropsMaxRunes {
						newPost.Props = props
					} else {
						if cfg.DiscardInvalidProps {
							t.Logger.Warn("Unable import post as props exceed the maximum character count. Skipping as --discard-invalid-props is enabled.")
							continue
						} else {
							t.Logger.Warn("Unable to add props to post as they exceed the maximum character count.")
						}
					}
				}

				AddPostToThreads(post, newPost, threads, channel, timestamps, cfg.ImportWorkflowMessages)

			// file comment
			case post.IsFileComment():
				if post.Comment == nil {
					t.Logger.Warn("Unable to import the message as it has no comments.")
					continue
				}
				if post.Comment.User == "" {
					t.Logger.Warn("Unable to import the message as the user field is missing.")
					continue
				}
				author := t.Intermediate.UsersById[post.Comment.User]
				if author == nil {
					t.Logger.Warnf("Unable to add the message as the Slack user does not exist in Mattermost. user=%s", post.Comment.User)
					continue
				}
				newPost := &IntermediatePost{
					User:     author.Username,
					Channel:  channel.Name,
					Message:  post.Comment.Comment,
					CreateAt: SlackConvertTimeStamp(post.TimeStamp),
				}

				AddPostToThreads(post, newPost, threads, channel, timestamps, cfg.ImportWorkflowMessages)

			// bot message
			case post.IsBotMessage():
				if !cfg.ImportWorkflowMessages {
					continue
				}
				author := t.selectOrCreateWorkflowUser(post)
				newPost := &IntermediatePost{
					User:     author.Username,
					Channel:  channel.Name,
					Message:  post.Text,
					CreateAt: SlackConvertTimeStamp(post.TimeStamp),
				}
				if (post.File != nil || post.Files != nil) && !cfg.SkipAttachments {
					if post.File != nil {
						err := addFileToPost(post.File, slackExport.Uploads, newPost, cfg.AttachmentsDir)
						if err != nil {
							t.Logger.WithError(err).Error("Failed to add file to post")
						}
					} else if post.Files != nil {
						for _, file := range post.Files {
							err := addFileToPost(file, slackExport.Uploads, newPost, cfg.AttachmentsDir)
							if err != nil {
								t.Logger.WithError(err).Error("Failed to add file to post")
							}
						}
					}
				}

				if len(post.Attachments) > 0 {
					props := model.StringInterface{"attachments": post.Attachments}
					propsB, _ := json.Marshal(props)

					if utf8.RuneCountInString(string(propsB)) <= model.PostPropsMaxRunes {
						newPost.Props = props
					} else {
						if cfg.DiscardInvalidProps {
							t.Logger.Warn("Unable import post as props exceed the maximum character count. Skipping as --discard-invalid-props is enabled.")
							continue
						} else {
							t.Logger.Warn("Unable to add props to post as they exceed the maximum character count.")
						}
					}
				}

				AddPostToThreads(post, newPost, threads, channel, timestamps, cfg.ImportWorkflowMessages)

			// channel join/leave messages
			case post.IsJoinLeaveMessage():
				// log.Println("Slack Import: Join/Leave messages are not yet supported")
				break

			// me message
			case post.IsMeMessage():
				// log.Println("Slack Import: me messages are not yet supported")
				break

			// change topic message
			case post.IsChannelTopicMessage():
				if post.User == "" {
					t.Logger.Warn("Unable to import the message as the user field is missing.")
					continue
				}
				author := t.Intermediate.UsersById[post.User]
				if author == nil {
					t.Logger.Warnf("Unable to add the message as the Slack user does not exist in Mattermost. user=%s", post.User)
					continue
				}

				newPost := &IntermediatePost{
					User:     author.Username,
					Channel:  channel.Name,
					Message:  post.Text,
					CreateAt: SlackConvertTimeStamp(post.TimeStamp),
					// Type:     model.POST_HEADER_CHANGE,
				}

				AddPostToThreads(post, newPost, threads, channel, timestamps, cfg.ImportWorkflowMessages)

			// change channel purpose message
			case post.IsChannelPurposeMessage():
				if post.User == "" {
					t.Logger.Warn("Unable to import the message as the user field is missing.")
					continue
				}
				author := t.Intermediate.UsersById[post.User]
				if author == nil {
					t.Logger.Warnf("Unable to add the message as the Slack user does not exist in Mattermost. user=%s", post.User)
					continue
				}

				newPost := &IntermediatePost{
					User:     author.Username,
					Channel:  channel.Name,
					Message:  post.Text,
					CreateAt: SlackConvertTimeStamp(post.TimeStamp),
					// Type:     model.POST_HEADER_CHANGE,
				}

				AddPostToThreads(post, newPost, threads, channel, timestamps, cfg.ImportWorkflowMessages)

			// change channel name message
			case post.IsChannelNameMessage():
				if post.User == "" {
					t.Logger.Warn("Slack Import: Unable to import the message as the user field is missing.")
					continue
				}
				author := t.Intermediate.UsersById[post.User]
				if author == nil {
					t.Logger.Warnf("Slack Import: Unable to add the message as the Slack user does not exist in Mattermost. user=%s", post.User)
					continue
				}

				newPost := &IntermediatePost{
					User:     author.Username,
					Channel:  channel.Name,
					Message:  post.Text,
					CreateAt: SlackConvertTimeStamp(post.TimeStamp),
					// Type:     model.POST_DISPLAYNAME_CHANGE,
				}

				AddPostToThreads(post, newPost, threads, channel, timestamps, cfg.ImportWorkflowMessages)

			default:
				t.Logger.Warnf("Unable to import the message as its type is not supported. post_type=%s, post_subtype=%s", post.Type, post.SubType)
			}
		}

		resultPosts = append(resultPosts, threads.GetChangedThreads()...)
	}

	t.Intermediate.Posts = resultPosts
	t.Intermediate.GroupChannels = append(t.Intermediate.GroupChannels, newGroupChannels...)
	t.Intermediate.DirectChannels = append(t.Intermediate.DirectChannels, newDirectChannels...)

	return nil
}

type TransformConfig struct {
	AttachmentsDir         string
	SkipAttachments        bool
	DiscardInvalidProps    bool
	AuthDataAsEmail        bool
	AuthService            string
	ImportWorkflowMessages bool
	SkipPosts              bool
	SkipChannels           bool
	RedisConfig            *RedisConfig
}

func (t *Transformer) Transform(cfg *TransformConfig, slackExport *SlackExport) error {
	t.TransformUsers(slackExport.Users, cfg.AuthDataAsEmail, cfg.AuthService)

	if !cfg.SkipChannels {
		if err := t.TransformAllChannels(slackExport); err != nil {
			return err
		}

		t.PopulateUserMemberships()
		t.PopulateChannelMemberships()
	}

	if !cfg.SkipPosts {
		if err := t.TransformPosts(cfg, slackExport); err != nil {
			return err
		}
	}

	return nil
}
