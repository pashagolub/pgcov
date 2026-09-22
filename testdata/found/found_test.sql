DO $$
BEGIN
    ASSERT found_after_perform(true), 'FOUND should be true';
    ASSERT NOT found_after_perform(false), 'FOUND should be false';
END;
$$;
