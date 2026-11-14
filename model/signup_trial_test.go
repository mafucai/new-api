package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetTrialTables clears the rows these tests create. truncateTables only
// registers a t.Cleanup hook, so it cannot be used to prepare a test case.
func resetTrialTables(t *testing.T) {
	t.Helper()
	require.NoError(t, DB.AutoMigrate(&User{}, &UserSubscription{}, &SubscriptionPlan{}, &Log{}))
	DB.Exec("DELETE FROM user_subscriptions")
	DB.Exec("DELETE FROM subscription_plans")
	DB.Exec("DELETE FROM logs")
	DB.Exec("DELETE FROM users")
	t.Cleanup(func() {
		DB.Exec("DELETE FROM user_subscriptions")
		DB.Exec("DELETE FROM subscription_plans")
		DB.Exec("DELETE FROM logs")
		DB.Exec("DELETE FROM users")
	})
}

func newTrialTestUser(t *testing.T, name string) *User {
	t.Helper()
	user := &User{
		Username:    name,
		Password:    "unused-password-hash",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AuthVersion: 1,
		AffCode:     name + "-aff",
	}
	require.NoError(t, DB.Create(user).Error)
	return user
}

func newTrialTestPlan(t *testing.T, upgradeGroup string) *SubscriptionPlan {
	t.Helper()
	overlap := false
	plan := &SubscriptionPlan{
		Title:               "signup trial",
		PriceAmount:         0,
		Currency:            "USD",
		DurationUnit:        "hour",
		DurationValue:       4,
		Enabled:             true,
		MaxPurchasePerUser:  1,
		UpgradeGroup:        upgradeGroup,
		TotalAmount:         0, // 0 = unlimited quota during the trial window
		AllowWalletOverflow: &overlap,
	}
	require.NoError(t, DB.Create(plan).Error)
	return plan
}

func TestGrantSignupTrialSubscriptionBindsPlanOnce(t *testing.T) {
	resetTrialTables(t)
	previous := common.SignupTrialPlanId
	t.Cleanup(func() { common.SignupTrialPlanId = previous })

	plan := newTrialTestPlan(t, "")
	user := newTrialTestUser(t, "trial-bind-user")
	common.SignupTrialPlanId = plan.Id

	GrantSignupTrialSubscription(user.Id)

	var subs []UserSubscription
	require.NoError(t, DB.Where("user_id = ?", user.Id).Find(&subs).Error)
	require.Len(t, subs, 1, "registration must create exactly one trial subscription")
	assert.Equal(t, plan.Id, subs[0].PlanId)
	assert.Equal(t, "signup_trial", subs[0].Source)
	assert.Equal(t, "active", subs[0].Status)
	// 4 hours from now, with a small tolerance for clock movement.
	expectedEnd := subs[0].StartTime + 4*3600
	assert.InDelta(t, expectedEnd, subs[0].EndTime, 5)

	// A second grant must not duplicate the subscription.
	GrantSignupTrialSubscription(user.Id)
	require.NoError(t, DB.Where("user_id = ?", user.Id).Find(&subs).Error)
	assert.Len(t, subs, 1)
}

func TestGrantSignupTrialSubscriptionDisabledWithoutPlan(t *testing.T) {
	resetTrialTables(t)
	previous := common.SignupTrialPlanId
	t.Cleanup(func() { common.SignupTrialPlanId = previous })

	user := newTrialTestUser(t, "trial-disabled-user")
	common.SignupTrialPlanId = 0

	GrantSignupTrialSubscription(user.Id)

	var count int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ?", user.Id).Count(&count).Error)
	assert.Zero(t, count, "no plan configured means no trial")
}

func TestGrantSignupTrialSubscriptionIgnoresMissingPlan(t *testing.T) {
	resetTrialTables(t)
	previous := common.SignupTrialPlanId
	t.Cleanup(func() { common.SignupTrialPlanId = previous })

	user := newTrialTestUser(t, "trial-missing-plan-user")
	common.SignupTrialPlanId = 999999

	// Must not panic or fail the caller; a broken plan reference cannot be
	// allowed to break registration.
	GrantSignupTrialSubscription(user.Id)

	var count int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ?", user.Id).Count(&count).Error)
	assert.Zero(t, count)
}

func TestGrantSignupTrialSubscriptionMovesUserToUpgradeGroup(t *testing.T) {
	resetTrialTables(t)
	previous := common.SignupTrialPlanId
	t.Cleanup(func() { common.SignupTrialPlanId = previous })

	plan := newTrialTestPlan(t, "default") // reuse an existing group
	user := newTrialTestUser(t, "trial-group-user")
	common.SignupTrialPlanId = plan.Id

	GrantSignupTrialSubscription(user.Id)

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, "default", got.Group)

	var sub UserSubscription
	require.NoError(t, DB.Where("user_id = ?", user.Id).First(&sub).Error)
	assert.Equal(t, "default", sub.UpgradeGroup)
}
