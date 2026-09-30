-- Scheduling state for periodic jobs. One row per job name, shared by every
-- replica: the row is both the claim ("who is taking this tick") and the
-- watermark ("how far the work has actually reached"), so the two can never
-- disagree.
--
-- Periodic work exists because a dropship vendor cannot call us; Vinculum
-- publishes no callback endpoint, so stock and dispatch are pulled.
CREATE TABLE scheduler_state (
    job_name        TEXT        PRIMARY KEY,
    watermark       TIMESTAMPTZ,
    next_due_at     TIMESTAMPTZ NOT NULL,
    last_fired_at   TIMESTAMPTZ,
    last_success_at TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON COLUMN scheduler_state.watermark IS
    'How far the last successful run covered. Work is bounded by this, not by when the process woke up, so a tick missed while the process was down is covered by the next run rather than skipped. NULL until the first success.';

COMMENT ON COLUMN scheduler_state.next_due_at IS
    'When the job may next be claimed. A claim is a conditional UPDATE on this column, which is what makes a tick fire exactly once across replicas.';

COMMENT ON COLUMN scheduler_state.last_fired_at IS
    'When a run last started. Far ahead of last_success_at means runs are starting and not finishing.';
