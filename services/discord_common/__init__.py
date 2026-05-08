"""Shared infrastructure for Discord-following sidecars.

Houses Discord DOM extraction and the one-time login bootstrap. Anything
that's invariant across channels and parser grammars lives here; per-channel
parsers, watchers, and emitters stay in their respective service dirs.
"""
