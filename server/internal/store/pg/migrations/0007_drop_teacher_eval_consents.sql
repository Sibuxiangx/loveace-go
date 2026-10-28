-- Teacher auto-evaluation no longer records consent in the backend: the
-- mini-program requires agreement at login. The table created in 0006 is
-- unused (zero rows in production at the time of the drop), so it is removed.

DROP TABLE IF EXISTS teacher_eval_consents;
