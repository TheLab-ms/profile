package chatbot

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"

	"github.com/TheLab-ms/profile/internal/conf"
	"github.com/bwmarrin/discordgo"
)

func Start(ctx context.Context, env *conf.Env) error {
	if env.DiscordAppID == "" {
		log.Printf("not starting discord bot because it isn't configured")
		return nil
	}

	s, err := discordgo.New("Bot " + env.DiscordBotToken)
	if err != nil {
		return err
	}

	_, err = s.ApplicationCommandCreate(env.DiscordAppID, env.DiscordGuildID, &discordgo.ApplicationCommand{
		Name:        "link",
		Description: "Link your membership to Discord",
		Type:        discordgo.ChatApplicationCommand,
	})
	if err != nil {
		return err
	}

	s.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		member := i.Member
		if member == nil || member.User == nil {
			return
		}
		id := member.User.ID

		log.Printf("got link request for discord user %q", id)
		signature := GenerateHMAC(id, env.DiscordBotToken)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Flags:   discordgo.MessageFlagsEphemeral,
				Content: fmt.Sprintf("[Go to the profile app to finish the process!](%s/link-discord?user=%s&sig=%s)", env.SelfURL, id, signature),
			},
		})
	})

	go func() {
		<-ctx.Done()
		s.Close()
	}()

	return s.Open()
}

func GenerateHMAC(message, key string) string {
	keyBytes := []byte(key)
	messageBytes := []byte(message)
	hmacObj := hmac.New(sha256.New, keyBytes)
	hmacObj.Write(messageBytes)
	hmacHash := hmacObj.Sum(nil)
	return hex.EncodeToString(hmacHash)
}
