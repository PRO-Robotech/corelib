// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package dropguard decides whether dropping a table is safe by MEASURING the
// table, not by arguing about it.
//
// # Why it exists
//
// Every table this repo has ever dropped was dropped on the strength of a prose
// paragraph in the migration: "nothing ever seeded this", "no tenant wrote here",
// "the join table went in an earlier migration rather than being copied". Each of
// those sentences was true when it was written. None of them is checked, so none of
// them stays true on its own, and the moment one stops being true nothing says so —
// the migration still runs, and the rows still go.
//
// A claim about data that no one counts is not a safety property. It is a note.
//
// # What a decision looks like here
//
// A drop is admissible when three things hold, and all three are established by
// reading the migrations and the database rather than by reading a comment:
//
//	declared  every DROP TABLE in an Up section has an entry in the service's
//	          dropguard.json naming the exact number of rows it expects to destroy.
//	          A drop nobody declared is a finding, so the number cannot be skipped;
//	          an entry with no drop behind it is also a finding, so the declaration
//	          expires by itself when the migration it describes is gone.
//
//	measured  the row count is read from the database at the version immediately
//	          before the drop, and compared for EQUALITY with the declared number.
//	          Not "at most", not "roughly": a table that grew a row nobody accounted
//	          for is exactly the case this exists to catch.
//
//	grounded  a declaration expecting rows must point at the migration that INSERTs
//	          them, and that INSERT is looked up in the parsed migrations. An
//	          expectation of rows in a table no migration ever writes to contradicts
//	          itself, and is refused.
//
// # Its own precondition
//
// The measurement refuses to produce a number it did not observe. If the database
// cannot be reached, or the table is not there to be counted, [Observe] returns
// [ErrNoConnection] or [ErrTableAbsent] — never (0, nil). This is the whole point:
// a guard that reports "zero rows" when it never connected is worse than no guard,
// because it reports the safe answer under precisely the conditions in which it
// knows nothing. Callers must render those two errors as NOT VERIFIED and refuse
// the drop, never as clean.
//
// # The other database: what a replayed chain cannot know
//
// The measured runs replay the migration chain into an empty database, so the
// numbers they produce are facts about the chain: what our own migrations seed. A
// tenant database also holds what tenants wrote, and no test container can know
// that.
//
// That half is [Preflight], reached through [Gate], and every migrator in the tree
// calls it before it applies anything. It counts, on the database in front of it,
// each table that a migration which has NOT YET RUN — and which THIS RUN WILL
// REACH — is going to drop. The question there is narrower than equality with a
// declared number twice over. That number was measured on a different database, and
// matching it licenses nothing here; and a run that stops at a version cannot
// destroy anything past it, so refusing over a drop it will not execute would cost
// an operator an approval for somebody else's table. The question is whether this
// drop destroys rows AT ALL, and a table that still holds any is refused until an
// operator names that exact drop.
//
// How far the run goes arrives as [Target], whose ZERO VALUE is the whole chain:
// the caller that forgets gets the widest check, and there is no "count nothing" to
// reach for — the same reason [Approval] names a version and a table and no blanket
// override exists.
//
// [Observe] is exported for exactly that reason: the same primitive, with the same
// refusal to guess, serves both halves. Whichever half asks, an unreachable database
// comes back as [ErrNoConnection] and never as a count of zero.
//
// The choice, its cost and what an operator does when a deploy stops are recorded
// once, in docs/architecture/drop-preflight-counts-the-live-database.md, and are
// deliberately not restated here.
//
// # What the reader can see, and what it cannot
//
// Both halves start from the same reading of the migrations, so the forms of drop
// that reading understands are a property of the whole gate. They are named here
// because a form nobody named is not judged clean — it is not judged at all, and
// the difference is invisible from the outside.
//
// Judged. The subject is written down, and the gate reads it:
//
//	DROP TABLE kaname.limits;                 plain, schema-qualified or not
//	DROP TABLE IF EXISTS a, b;                every name in a list, each with its own line
//	EXECUTE 'DROP TABLE kaname.limits';       dynamic, but the statement is a LITERAL
//
// The third is worth spelling out because it is easy to assume otherwise: putting a
// drop inside EXECUTE does not hide it, as long as the name is still written. What
// hides a drop is the name being COMPUTED, not the EXECUTE around it.
//
// Seen but not judged, and COUNTED as such. The statement is recognisable as a drop,
// but its subject is assembled at run time, so the file holds no name to read:
//
//	EXECUTE format('DROP TABLE %I', t);       placeholder
//	EXECUTE format('DROP TABLE sch.%I', t);   qualifier written, leaf computed
//	EXECUTE 'DROP TABLE ' || quote_ident(t);  concatenation
//
// No pattern can close this. The name does not exist until PL/pgSQL builds the
// string, and a gate that guessed at it would be asserting something it did not
// read — the exact move this package was written to stop. So these are counted by
// [Inv.UnreadableDrops], and both censuses print them with coordinates. That is the
// honest position: the gate does not say such a drop is safe, it says it could not
// read it. The second form would otherwise be WORSE than unread — dropTableRe
// captures `sch.` and would enter a table that does not exist into the inventory —
// and it is suppressed for that reason.
//
// Not seen at all. The text `DROP TABLE` is not in the file:
//
//	EXECUTE stmt;                             statement built elsewhere, or read from a query
//
// This one is not in the count either, and saying so is the point of naming it: a
// number cannot include what nothing detected. Nothing short of executing the
// migration would reveal it, which is why [Preflight] counts the live database
// before a run rather than trusting this reading alone — but a drop built entirely
// out of values would still pass unremarked, and no part of this package claims
// otherwise.
//
// The idiom is not hypothetical. This tree runs dynamic DDL in migrations already —
// DROP INDEX and ALTER TABLE ... DROP CONSTRAINT are built this way — so the first
// table dropped in that style would land in a form nothing here reads. Whether an
// unreadable drop should REFUSE a migration rather than be counted is a policy
// question this package does not settle; it is not settled by silence either, which
// is why the number is printed even when it is zero.
//
// # What neither half covers
//
// Neither is atomic with the drop. The live count is taken seconds before the
// migration runs, while the old pods are still serving, so a row can arrive in
// between. That makes the answer recent rather than never-taken; nothing available
// here would make it simultaneous, and claiming otherwise would be worse than not
// counting.
package dropguard
