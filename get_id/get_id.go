package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"github.com/joho/godotenv"
	"golang.org/x/term"
)

// termAuth реализует интерфейс auth.UserAuthenticator
// Он нужен, чтобы запрашивать данные для входа прямо в терминале.
type termAuth struct{}

func (a termAuth) Phone(_ context.Context) (string, error) {
	fmt.Print("📱 Введите номер телефона (в международном формате, например +79...): ")
	reader := bufio.NewReader(os.Stdin)
	phone, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(phone), nil
}

func (a termAuth) Password(_ context.Context) (string, error) {
	fmt.Print("🔐 Введите 2FA пароль (если установлен, иначе просто нажмите Enter): ")
	bytePwd, err := term.ReadPassword(int(os.Stdin.Fd()))
	if err != nil {
		return "", err
	}
	fmt.Println()
	return strings.TrimSpace(string(bytePwd)), nil
}

func (a termAuth) AcceptTermsOfService(_ context.Context, tos tg.HelpTermsOfService) error {
	return &auth.SignUpRequired{TermsOfService: tos}
}

func (a termAuth) SignUp(_ context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("регистрация новых аккаунтов не поддерживается этим скриптом")
}

func (a termAuth) Code(_ context.Context, _ *tg.AuthSentCode) (string, error) {
	fmt.Print("✉️  Введите код подтверждения из Telegram: ")
	reader := bufio.NewReader(os.Stdin)
	code, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(code), nil
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ Предупреждение: .env не найден, использую системные переменные")
	}

	apiIDStr := os.Getenv("API_ID")
	apiHash := os.Getenv("API_HASH")

	if apiIDStr == "" || apiHash == "" {
		log.Fatal("❌ ОШИБКА: API_ID или API_HASH не заданы в .env")
	}

	apiID, err := strconv.Atoi(apiIDStr)
	if err != nil {
		log.Fatalf("❌ ОШИБКА: API_ID должен быть числом: %v", err)
	}

	// Создаем директорию для сессии, иначе FileStorage выдаст ошибку
	if err := os.MkdirAll("./data", 0755); err != nil {
		log.Fatalf("❌ Ошибка создания папки ./data: %v", err)
	}

	sessionPath := "./data/session.json"

	client := telegram.NewClient(apiID, apiHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: sessionPath},
	})

	fmt.Println("⏳ Подключение к Telegram...")

	err = client.Run(context.Background(), func(ctx context.Context) error {
		// 1. АВТОРИЗАЦИЯ
		// Этот блок проверит, есть ли валидная сессия. Если нет - запустит методы из termAuth
		flow := auth.NewFlow(termAuth{}, auth.SendCodeOptions{})
		if err := client.Auth().IfNecessary(ctx, flow); err != nil {
			return fmt.Errorf("ошибка авторизации: %w", err)
		}

		fmt.Println("✅ Авторизация успешна! Получаю список диалогов...")

		// 2. ПОЛУЧЕНИЕ ДАННЫХ
		api := client.API()

		dialogs, err := api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
			OffsetPeer: &tg.InputPeerEmpty{},
			Limit:      500,
		})
		if err != nil {
			return fmt.Errorf("ошибка получения диалогов: %w", err)
		}

		var chats []tg.ChatClass

		switch d := dialogs.(type) {
		case *tg.MessagesDialogs:
			chats = d.Chats
		case *tg.MessagesDialogsSlice:
			chats = d.Chats
		default:
			return fmt.Errorf("неизвестный тип ответа от Telegram: %T", d)
		}

		fmt.Printf("\n✅ Найдено чатов/каналов: %d\n", len(chats))
		fmt.Println(strings.Repeat("=", 60))

		var latestIDs []string
		for _, chatClass := range chats {
			switch c := chatClass.(type) {
			case *tg.Chat:
				fmt.Printf("[Группа] %-30s | %d\n", truncate(c.Title, 29), c.ID)
				latestIDs = append(latestIDs, strconv.FormatInt(c.ID, 10))
			case *tg.Channel:
				if c.Megagroup {
					fmt.Printf("[Супергруппа] %-25s | %d | %s\n", truncate(c.Title, 24), c.ID, formatUsername(c.Username))
				} else {
					fmt.Printf("[Канал] %-31s | %d | %s\n", truncate(c.Title, 30), c.ID, formatUsername(c.Username))
				}
				latestIDs = append(latestIDs, strconv.FormatInt(c.ID, 10))
			case *tg.ChatForbidden:
				fmt.Printf("[Нет доступа] %-25s | %d\n", truncate(c.Title, 24), c.ID)
				latestIDs = append(latestIDs, strconv.FormatInt(c.ID, 10))
			case *tg.ChannelForbidden:
				fmt.Printf("[Нет доступа] %-25s | %d\n", truncate(c.Title, 24), c.ID)
				latestIDs = append(latestIDs, strconv.FormatInt(c.ID, 10))
			}
		}
		fmt.Println(strings.Repeat("=", 60))
		if err := os.WriteFile("latest_chat_ids.txt", []byte(strings.Join(latestIDs, "\n")+"\n"), 0644); err != nil {
			return fmt.Errorf("ошибка записи latest_chat_ids.txt: %w", err)
		}

		return nil
	})

	if err != nil {
		log.Fatalf("❌ Критическая ошибка: %v", err)
	}
}

// truncate теперь работает с рунами (юникодом), чтобы русские буквы
// обрезались корректно и не ломали вывод.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-3]) + "..."
}

func formatUsername(username string) string {
	username = strings.TrimSpace(username)
	if username == "" {
		return "-"
	}
	return "@" + username
}
