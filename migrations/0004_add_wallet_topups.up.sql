CREATE TABLE wallet_topups (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    wallet_id           UUID NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
    amount              NUMERIC(12,2) NOT NULL CHECK (amount > 0),
    currency            VARCHAR(10) NOT NULL DEFAULT 'INR',
    status              VARCHAR(20) NOT NULL DEFAULT 'pending',
    gateway_order_id    VARCHAR(100) UNIQUE NOT NULL,
    gateway_payment_id  VARCHAR(100),
    created_at          TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at          TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX wallet_topups_user_id_idx ON wallet_topups (user_id);
