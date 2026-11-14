package model

import (
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSearchRedemptionsFiltersAndPaginates(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&Redemption{}, &User{}))
	require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&Redemption{}).Error)
	t.Cleanup(func() {
		require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&Redemption{}).Error)
	})

	now := common.GetTimestamp()
	redemptions := []Redemption{
		{Id: 1, Name: "alpha-active", Key: "00000000000000000000000000000001", Status: common.RedemptionCodeStatusEnabled, ExpiredTime: 0},
		{Id: 2, Name: "alpha-future", Key: "00000000000000000000000000000002", Status: common.RedemptionCodeStatusEnabled, ExpiredTime: now + 3600},
		{Id: 3, Name: "alpha-expired", Key: "00000000000000000000000000000003", Status: common.RedemptionCodeStatusEnabled, ExpiredTime: now - 10},
		{Id: 4, Name: "beta-disabled", Key: "00000000000000000000000000000004", Status: common.RedemptionCodeStatusDisabled, ExpiredTime: 0},
		{Id: 5, Name: "beta-used", Key: "00000000000000000000000000000005", Status: common.RedemptionCodeStatusUsed, ExpiredTime: 0},
	}
	require.NoError(t, DB.Create(&redemptions).Error)

	tests := []struct {
		name      string
		keyword   string
		status    string
		startIdx  int
		num       int
		wantTotal int64
		wantIds   []int
	}{
		{
			name:      "no filters returns all rows",
			num:       10,
			wantTotal: 5,
			wantIds:   []int{5, 4, 3, 2, 1},
		},
		{
			name:      "keyword filters by name prefix",
			keyword:   "alpha",
			num:       10,
			wantTotal: 3,
			wantIds:   []int{3, 2, 1},
		},
		{
			name:      "enabled status excludes expired rows",
			status:    "1",
			num:       10,
			wantTotal: 2,
			wantIds:   []int{2, 1},
		},
		{
			name:      "expired status returns enabled expired rows",
			status:    "expired",
			num:       10,
			wantTotal: 1,
			wantIds:   []int{3},
		},
		{
			name:      "disabled status",
			status:    "2",
			num:       10,
			wantTotal: 1,
			wantIds:   []int{4},
		},
		{
			name:      "used status",
			status:    "3",
			num:       10,
			wantTotal: 1,
			wantIds:   []int{5},
		},
		{
			name:      "pagination keeps unpaged total",
			startIdx:  1,
			num:       2,
			wantTotal: 5,
			wantIds:   []int{4, 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, total, err := SearchRedemptions(tt.keyword, tt.status, tt.startIdx, tt.num)
			require.NoError(t, err)
			assert.Equal(t, tt.wantTotal, total)
			gotIds := make([]int, 0, len(rows))
			for _, row := range rows {
				gotIds = append(gotIds, row.Id)
			}
			assert.Equal(t, tt.wantIds, gotIds)
		})
	}
}

func setupRedeemFixture(t *testing.T, quota int) (userId int, key string) {
	t.Helper()
	require.NoError(t, DB.AutoMigrate(&Redemption{}, &User{}))
	require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&Redemption{}).Error)
	t.Cleanup(func() {
		require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&Redemption{}).Error)
		DB.Exec("DELETE FROM users")
		DB.Exec("DELETE FROM logs")
	})

	user := &User{Username: "redeem-user", Password: "password", Status: common.UserStatusEnabled, Quota: 0}
	require.NoError(t, DB.Create(user).Error)

	key = "10000000000000000000000000000001"
	redemption := &Redemption{
		Name:        "redeem-test",
		Key:         key,
		Status:      common.RedemptionCodeStatusEnabled,
		Quota:       quota,
		CreatedTime: common.GetTimestamp(),
	}
	require.NoError(t, DB.Create(redemption).Error)
	return user.Id, key
}

func TestRedeemCreditsFirstTopupRebatesInviterOnce(t *testing.T) {
	setupRedeemFixture(t, 1000)

	inviter := &User{Username: "inviter", Password: "password", Status: common.UserStatusEnabled, Quota: 0, AffCode: "inviter-aff"}
	invitee := &User{Username: "invitee", Password: "password", Status: common.UserStatusEnabled, Quota: 0, AffCode: "invitee-aff"}
	require.NoError(t, DB.Create(inviter).Error)
	invitee.InviterId = inviter.Id
	require.NoError(t, DB.Create(invitee).Error)

	key1 := "20000000000000000000000000000001"
	key2 := "20000000000000000000000000000002"
	require.NoError(t, DB.Create(&Redemption{Name: "rebate-1", Key: key1, Status: common.RedemptionCodeStatusEnabled, Quota: 1000, CreatedTime: common.GetTimestamp()}).Error)
	require.NoError(t, DB.Create(&Redemption{Name: "rebate-2", Key: key2, Status: common.RedemptionCodeStatusEnabled, Quota: 2000, CreatedTime: common.GetTimestamp()}).Error)

	_, err := Redeem(key1, invitee.Id)
	require.NoError(t, err)
	_, err = Redeem(key2, invitee.Id)
	require.NoError(t, err)

	var got User
	require.NoError(t, DB.First(&got, inviter.Id).Error)
	assert.Equal(t, 100, got.AffQuota)
	assert.Equal(t, 100, got.AffHistoryQuota)
}

// A deleted inviter must never block the invitee's own top-up: the payer is
// credited, the rebate is skipped, and the one-shot claim stays releasable.
func TestRedeemSucceedsWhenInviterIsGone(t *testing.T) {
	userId, key := setupRedeemFixture(t, 500)

	invitee := &User{Username: "orphan-invitee", Password: "password", Status: common.UserStatusEnabled, Quota: 0, AffCode: "orphan-aff"}
	require.NoError(t, DB.Create(invitee).Error)
	invitee.InviterId = 999999
	require.NoError(t, DB.Model(&User{}).Where("id = ?", invitee.Id).Update("inviter_id", 999999).Error)

	redeemKey := "20000000000000000000000000000010"
	require.NoError(t, DB.Create(&Redemption{Name: "orphan-rebate", Key: redeemKey, Status: common.RedemptionCodeStatusEnabled, Quota: 1000, CreatedTime: common.GetTimestamp()}).Error)

	quota, err := Redeem(redeemKey, invitee.Id)
	require.NoError(t, err)
	assert.Equal(t, 1000, quota)

	var paid User
	require.NoError(t, DB.First(&paid, invitee.Id).Error)
	assert.Equal(t, 1000, paid.Quota, "payer must keep the credit")
	assert.Equal(t, 0, paid.FirstTopupRebated, "claim must be released so a real inviter can still be paid")

	_ = userId
	_ = key
}

// A rebate that cannot be paid must not consume the one-shot claim, so a later
// top-up with a valid inviter still pays.
func TestRedeemRebateClaimReleasedWhenInviterMissing(t *testing.T) {
	setupRedeemFixture(t, 500)

	invitee := &User{Username: "retry-invitee", Password: "password", Status: common.UserStatusEnabled, Quota: 0, AffCode: "retry-aff"}
	require.NoError(t, DB.Create(invitee).Error)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", invitee.Id).Update("inviter_id", 888888).Error)

	key := "20000000000000000000000000000011"
	require.NoError(t, DB.Create(&Redemption{Name: "retry-rebate", Key: key, Status: common.RedemptionCodeStatusEnabled, Quota: 1000, CreatedTime: common.GetTimestamp()}).Error)

	_, err := Redeem(key, invitee.Id)
	require.NoError(t, err)

	var got User
	require.NoError(t, DB.First(&got, invitee.Id).Error)
	assert.Equal(t, 0, got.FirstTopupRebated)

	// Now attach a real inviter; the next top-up pays the first-time rebate.
	inviter := &User{Username: "late-inviter", Password: "password", Status: common.UserStatusEnabled, Quota: 0, AffCode: "late-aff"}
	require.NoError(t, DB.Create(inviter).Error)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", invitee.Id).Update("inviter_id", inviter.Id).Error)

	key2 := "20000000000000000000000000000012"
	require.NoError(t, DB.Create(&Redemption{Name: "retry-rebate-2", Key: key2, Status: common.RedemptionCodeStatusEnabled, Quota: 1000, CreatedTime: common.GetTimestamp()}).Error)
	_, err = Redeem(key2, invitee.Id)
	require.NoError(t, err)

	var paid User
	require.NoError(t, DB.First(&paid, inviter.Id).Error)
	assert.Equal(t, 100, paid.AffQuota)
}

// Rows created before the column existed hold NULL. A NULL claim must still be
// treated as "not yet rebated", otherwise those users never pay their inviter.
func TestRedeemRebatesWhenClaimColumnIsNull(t *testing.T) {
	setupRedeemFixture(t, 500)

	inviter := &User{Username: "null-inviter", Password: "password", Status: common.UserStatusEnabled, Quota: 0, AffCode: "null-inviter-aff"}
	invitee := &User{Username: "null-invitee", Password: "password", Status: common.UserStatusEnabled, Quota: 0, AffCode: "null-invitee-aff"}
	require.NoError(t, DB.Create(inviter).Error)
	require.NoError(t, DB.Create(invitee).Error)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", invitee.Id).
		Updates(map[string]any{"inviter_id": inviter.Id, "first_topup_rebated": nil}).Error)

	key := "20000000000000000000000000000013"
	require.NoError(t, DB.Create(&Redemption{Name: "null-rebate", Key: key, Status: common.RedemptionCodeStatusEnabled, Quota: 1000, CreatedTime: common.GetTimestamp()}).Error)

	_, err := Redeem(key, invitee.Id)
	require.NoError(t, err)

	var paid User
	require.NoError(t, DB.First(&paid, inviter.Id).Error)
	assert.Equal(t, 100, paid.AffQuota, "a NULL claim must still pay the inviter")
}

func TestRedeemCreditsQuotaExactlyOnce(t *testing.T) {
	userId, key := setupRedeemFixture(t, 500)

	quota, err := Redeem(key, userId)
	require.NoError(t, err)
	assert.Equal(t, 500, quota)

	var user User
	require.NoError(t, DB.First(&user, "id = ?", userId).Error)
	assert.Equal(t, 500, user.Quota)

	var redemption Redemption
	require.NoError(t, DB.First(&redemption, "name = ?", "redeem-test").Error)
	assert.Equal(t, common.RedemptionCodeStatusUsed, redemption.Status)
	assert.Equal(t, userId, redemption.UsedUserId)

	// Redeeming the same code again must fail and must not credit quota.
	_, err = Redeem(key, userId)
	require.Error(t, err)
	require.NoError(t, DB.First(&user, "id = ?", userId).Error)
	assert.Equal(t, 500, user.Quota)
}

func TestRedeemRejectsWalletOverflow(t *testing.T) {
	userId, key := setupRedeemFixture(t, 11)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", userId).Update("quota", common.MaxWalletQuota-10).Error)

	_, err := Redeem(key, userId)
	require.ErrorIs(t, err, ErrRedeemFailed)

	var user User
	require.NoError(t, DB.First(&user, "id = ?", userId).Error)
	assert.Equal(t, common.MaxWalletQuota-10, user.Quota)

	var redemption Redemption
	require.NoError(t, DB.First(&redemption, "key = ?", key).Error)
	assert.Equal(t, common.RedemptionCodeStatusEnabled, redemption.Status)
}

func TestRedemptionQuotaRejectsWalletOverflow(t *testing.T) {
	setupRedeemFixture(t, 500)

	redemption := &Redemption{
		Name:        "overflow-redemption",
		Key:         "10000000000000000000000000000002",
		Status:      common.RedemptionCodeStatusEnabled,
		Quota:       common.MaxWalletQuota + 1,
		CreatedTime: common.GetTimestamp(),
	}
	require.Error(t, redemption.Insert())
}

// Exactly one of several concurrent redeems of the same code may win, and
// quota must be credited exactly once.
func TestRedeemConcurrentSingleSuccess(t *testing.T) {
	userId, key := setupRedeemFixture(t, 300)

	const goroutines = 5
	successes := make([]bool, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			if _, err := Redeem(key, userId); err == nil {
				successes[idx] = true
			}
		}(i)
	}
	wg.Wait()

	successCount := 0
	for _, ok := range successes {
		if ok {
			successCount++
		}
	}
	assert.Equal(t, 1, successCount, "exactly one concurrent redeem should succeed")

	var user User
	require.NoError(t, DB.First(&user, "id = ?", userId).Error)
	assert.Equal(t, 300, user.Quota, "quota must be credited exactly once")
}
