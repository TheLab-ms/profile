package chatbot

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"

	"github.com/TheLab-ms/profile/internal/conf"
	"github.com/TheLab-ms/profile/internal/reporting"
	"github.com/bwmarrin/discordgo"
)

type Bot struct {
	client *discordgo.Session
	env    *conf.Env
}

func NewBot(env *conf.Env) (*Bot, error) {
	b := &Bot{env: env}
	if env.DiscordAppID == "" {
		return b, nil
	}

	s, err := discordgo.New("Bot " + env.DiscordBotToken)
	if err != nil {
		return nil, err
	}
	b.client = s

	return b, nil
}

func (b *Bot) Start(ctx context.Context) error {
	if b.client == nil {
		log.Printf("not starting discord bot because it isn't configured")
		return nil
	}

	_, err := b.client.ApplicationCommandCreate(b.env.DiscordAppID, b.env.DiscordGuildID, &discordgo.ApplicationCommand{
		Name:        "link",
		Description: "Link your membership to Discord",
		Type:        discordgo.ChatApplicationCommand,
	})
	if err != nil {
		return err
	}

	b.client.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		member := i.Member
		if member == nil || member.User == nil {
			return
		}
		id := member.User.ID

		log.Printf("got link request for discord user %q", id)
		signature := GenerateHMAC(id, b.env.DiscordBotToken)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Flags:   discordgo.MessageFlagsEphemeral,
				Content: fmt.Sprintf("[Go to the profile app to finish the process!](%s/link-discord?user=%s&sig=%s)", b.env.SelfURL, id, signature),
			},
		})
	})

	go func() {
		<-ctx.Done()
		b.client.Close()
	}()

	return b.client.Open()
}

func (b *Bot) SyncUser(ctx context.Context, user *UserStatus) error {
	member, err := b.client.GuildMember(b.env.DiscordGuildID, strconv.FormatInt(user.ID, 10), discordgo.WithContext(ctx))
	if err != nil {
		if e, ok := err.(*discordgo.RESTError); ok && e.Response.StatusCode == 404 {
			return nil // nothing to do if the user no longer exists
		}
		return fmt.Errorf("getting guild member: %w", err)
	}

	var exists bool
	for _, role := range member.Roles {
		if role == b.env.DiscordMemberRoleID {
			exists = true
			break
		}
	}
	if exists == user.ActiveMember {
		return nil // already in sync
	}

	if user.ActiveMember {
		err = b.client.GuildMemberRoleAdd(b.env.DiscordGuildID, strconv.FormatInt(user.ID, 10), b.env.DiscordMemberRoleID, discordgo.WithContext(ctx))
		if err != nil {
			return fmt.Errorf("adding role to guild member %q: %w", member.DisplayName(), err)
		}
		log.Printf("added member role for discord user %s i.e. member %s", member.DisplayName(), user.Email)
		reporting.DefaultSink.Eventf(user.Email, "MembershipRoleDiverged", "added member role to discord user")
		return nil
	}

	err = b.client.GuildMemberRoleRemove(b.env.DiscordGuildID, strconv.FormatInt(user.ID, 10), b.env.DiscordMemberRoleID, discordgo.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("removing role from guild member %q: %w", member.DisplayName(), err)
	}
	log.Printf("removed member role from discord user %s i.e. member %s", member.DisplayName(), user.Email)
	reporting.DefaultSink.Eventf(user.Email, "MembershipRoleDiverged", "removed member role from discord user")
	return nil
}

func (b *Bot) ListUsers(ctx context.Context, cursor func(int64)) error {
	var after string
	for {
		members, err := b.client.GuildMembers(b.env.DiscordGuildID, after, 500, discordgo.WithContext(ctx))
		if err != nil {
			return err
		}
		if len(members) == 0 {
			return nil
		}
		for _, member := range members {
			after = member.User.ID
			if member.User == nil {
				continue // shouldn't be possible
			}
			id, err := strconv.ParseInt(member.User.ID, 10, 0)
			if err != nil {
				log.Printf("error while parsing discord member id: %s", err)
				continue
			}
			cursor(id)
		}
	}
}

type UserStatus struct {
	ID           int64
	Email        string
	ActiveMember bool
}

func GenerateHMAC(message, key string) string {
	keyBytes := []byte(key)
	messageBytes := []byte(message)
	hmacObj := hmac.New(sha256.New, keyBytes)
	hmacObj.Write(messageBytes)
	hmacHash := hmacObj.Sum(nil)
	return hex.EncodeToString(hmacHash)
}
