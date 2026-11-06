package chatbi

import (
	"context"
	"crypto/md5" // Only for checking imported SQLBot digests before replacement.
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"math"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

var ErrInvalidAccountChange = errors.New("invalid account change")
var ErrInitialPasswordDisabled = errors.New("initial password is disabled")

func passwordMatches(user User, password string) bool {
	if password == "" || len(password) > 512 {
		return false
	}
	switch user.PasswordFormat {
	case "bcrypt":
		return bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) == nil
	case "sqlbot-md5":
		digest := md5.Sum([]byte(password))
		expected, err := hex.DecodeString(user.PasswordHash)
		return err == nil && subtle.ConstantTimeCompare(digest[:], expected) == 1
	default:
		return false
	}
}

func (identity *Identity) changePassword(ctx context.Context, id ID, account, current, replacement, initialPassword string) error {
	if !id.Valid() || account == "" || current == "" || len(replacement) < 12 || len(replacement) > 72 {
		return ErrInvalidAccountChange
	}
	user, revision, err := identity.store.User(ctx, id)
	if err != nil || user.Account != account || user.Origin != 0 || !user.canUse(user.Workspace) || !passwordMatches(user, current) {
		return ErrUnauthenticated
	}
	settings, err := identity.store.ChatSettings(ctx)
	if err != nil {
		return err
	}
	if settings.InitialPasswordDisabled && initialPassword != "" && replacement == initialPassword {
		return ErrInitialPasswordDisabled
	}
	if replacement == current {
		return ErrInvalidAccountChange
	}
	hash, err := HashPassword(replacement)
	if err != nil {
		return ErrInvalidAccountChange
	}
	if user.AuthRevision == math.MaxInt64 {
		return ErrUnavailable
	}
	user.PasswordHash, user.PasswordFormat = hash, "bcrypt"
	user.AuthRevision++
	user.AllowLegacyTokens = false
	return identity.store.writeSemantic(ctx, userKey(id), user, revision, "user.password.change")
}

// ChangePassword invalidates every previous ordinary token after an audited
// compare-and-swap write. Embedded application tokens are not account tokens.
func (identity *Identity) ChangePassword(ctx context.Context, p Principal, current, replacement, initialPassword string) error {
	if p.EmbeddedApp != "" || !p.User.canUse(p.Workspace) {
		return ErrUnauthenticated
	}
	return identity.changePassword(ctx, p.User.ID, p.User.Account, current, replacement, initialPassword)
}

// ChangePasswordByAccount supports the original first-login password-change
// endpoint without issuing a session before the old password is verified.
func (identity *Identity) ChangePasswordByAccount(ctx context.Context, account, current, replacement, initialPassword string) error {
	if account == "" || len(account) > 100 {
		return ErrUnauthenticated
	}
	var id ID
	if _, err := identity.store.get(ctx, identity.store.accountKey(account), &id); err != nil {
		return ErrUnauthenticated
	}
	err := identity.changePassword(ctx, id, account, current, replacement, initialPassword)
	if errors.Is(err, ErrInvalidAccountChange) {
		return ErrUnauthenticated // Avoid revealing account existence to an unauthenticated caller.
	}
	return err
}

func (identity *Identity) ChangeLanguage(ctx context.Context, p Principal, language string) error {
	if p.EmbeddedApp != "" || !p.User.canUse(p.Workspace) {
		return ErrUnauthenticated
	}
	language = strings.TrimSpace(language)
	if language != "zh-CN" && language != "zh-TW" && language != "en-US" && language != "en" && language != "ko-KR" && language != "th-TH" {
		return ErrInvalidAccountChange
	}
	user, revision, err := identity.store.User(ctx, p.User.ID)
	if err != nil || user.Account != p.User.Account || user.AuthRevision != p.User.AuthRevision || !user.canUse(p.Workspace) {
		return ErrUnauthenticated
	}
	user.Language = language
	return identity.store.writeSemantic(ctx, userKey(user.ID), user, revision, "user.language.change")
}
