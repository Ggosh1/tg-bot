package auth

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	tgAuth "github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

type Terminal struct{}

func readLine(prompt string) string {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	text, _ := reader.ReadString('\n')
	return strings.TrimSpace(text)
}

func (t Terminal) Phone(_ context.Context) (string, error) {
	return readLine("📱 Введите номер телефона (например, +79991234567): "), nil
}
func (t Terminal) Password(_ context.Context) (string, error) {
	return readLine("🔑 Введите 2FA пароль (если есть): "), nil
}
func (t Terminal) AcceptTermsOfService(_ context.Context, tos tg.HelpTermsOfService) error { return nil }
func (t Terminal) SignUp(_ context.Context) (tgAuth.UserInfo, error) {
	return tgAuth.UserInfo{}, fmt.Errorf("регистрация не поддерживается")
}
func (t Terminal) Code(_ context.Context, sentCode *tg.AuthSentCode) (string, error) {
	return readLine("📩 Введите код из Telegram: "), nil
}