CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Identity
    full_name           VARCHAR(100),
    email               VARCHAR(255) UNIQUE,
    phone_number        VARCHAR(20) UNIQUE NOT NULL,

    -- Auth (external provider friendly)
    auth_provider       VARCHAR(50) NOT NULL DEFAULT 'password',

    -- Account status
    is_email_verified   BOOLEAN DEFAULT FALSE,
    is_phone_verified   BOOLEAN DEFAULT FALSE,
    status              VARCHAR(20) DEFAULT 'active',
    -- active | blocked | deleted

    -- Preferences
    timezone            VARCHAR(50) DEFAULT 'Asia/Kolkata',

    -- Audit
    created_at          TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at          TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE TABLE wallets (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Ownership
    user_id         UUID UNIQUE NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    -- Balance
    balance         NUMERIC(12,2) NOT NULL DEFAULT 0.00,
    currency        VARCHAR(10) NOT NULL DEFAULT 'INR',

    -- Status & control
    status          VARCHAR(20) NOT NULL DEFAULT 'active',
    -- active | blocked | suspended

    -- Safety & audit
    locked_amount   NUMERIC(12,2) NOT NULL DEFAULT 0.00,
    -- amount temporarily reserved during charging

    created_at      TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at      TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE TABLE wallet_transactions (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    wallet_id           UUID NOT NULL
                            REFERENCES wallets(id) ON DELETE CASCADE,

    -- Transaction nature
    transaction_type    VARCHAR(20) NOT NULL,
    -- CREDIT | DEBIT | LOCK | UNLOCK | REFUND | ADJUSTMENT

    amount              NUMERIC(12,2) NOT NULL CHECK (amount > 0),

    currency            VARCHAR(10) NOT NULL DEFAULT 'INR',

    -- Business context
    source              VARCHAR(30) NOT NULL,
    -- topup | charging | refund | admin | system

    reference_id        VARCHAR(100),
    -- payment_id / charging_session_id / admin_ticket_id

    idempotency_key     VARCHAR(100) UNIQUE,
    -- prevents double credit/debit (VERY IMPORTANT)

    -- State
    status              VARCHAR(20) NOT NULL DEFAULT 'success',
    -- pending | success | failed

    description         TEXT,

    created_at          TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);
