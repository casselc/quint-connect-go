// Two-phase commit in Choreo (informalsystems/choreo), after its
// two_phase_commit example: a coordinator and participants exchange
// messages through choreo's message soup. Choreo's own step picks a node
// and one of its transitions; a recorded run or a model-based test names
// both, so each transition is also a named action of the node that takes
// it (choreo::step_with): prepare(node), decideCommit, and so on. step
// chooses among the named actions, so `quint run --mbt` reports which one
// it took and with which node.
module twophase {
  import basicSpells.* from "spells/basicSpells"
  import choreo(processes = NODES) as choreo from "choreo"

  const PARTICIPANTS: Set[str]
  pure val COORDINATOR: str = "c"
  pure val NODES = PARTICIPANTS.union(Set(COORDINATOR))

  type Role = Coordinator | Participant
  type Stage = Working | Prepared | Committed | Aborted

  type Node = str
  type Message =
    | CoordinatorAbort
    | CoordinatorCommit
    | ParticipantPrepared(Node)

  type StateFields = { role: Role, stage: Stage }
  type CustomEffects = ()
  type Event = ()
  type Extensions = ()

  type LocalState = choreo::LocalState[Node, StateFields]
  type LocalContext = choreo::LocalContext[Node, StateFields, Message, Event, Extensions]
  type Transition = choreo::Transition[Node, StateFields, Message, Event, CustomEffects]

  pure def preparedIn(msgs: Set[Message]): Set[Node] =
    msgs.filterMap(m => match m {
      | ParticipantPrepared(n) => Some(n)
      | _ => None
    })

  // --- Participant transitions
  pure def spontaneously_prepares(ctx: LocalContext): Set[Transition] =
    if (ctx.state.role == Participant and ctx.state.stage == Working)
      Set({ effects: Set(choreo::Broadcast(ParticipantPrepared(ctx.state.process_id))), post_state: { ...ctx.state, stage: Prepared } })
    else Set()

  // A participant may refuse only before it has prepared: once it voted to
  // commit, it must wait for the coordinator.
  pure def spontaneously_aborts(ctx: LocalContext): Set[Transition] =
    if (ctx.state.role == Participant and ctx.state.stage == Working)
      Set({ effects: Set(), post_state: { ...ctx.state, stage: Aborted } })
    else Set()

  pure def aborts_as_instructed(ctx: LocalContext): Set[Transition] =
    if (ctx.state.role == Participant and ctx.state.stage != Aborted and ctx.messages.contains(CoordinatorAbort))
      Set({ effects: Set(), post_state: { ...ctx.state, stage: Aborted } })
    else Set()

  pure def commits_as_instructed(ctx: LocalContext): Set[Transition] =
    if (ctx.state.role == Participant and ctx.state.stage == Prepared and ctx.messages.contains(CoordinatorCommit))
      Set({ effects: Set(), post_state: { ...ctx.state, stage: Committed } })
    else Set()

  // --- Coordinator transitions
  pure def decides_on_commit(ctx: LocalContext): Set[Transition] =
    if (ctx.state.role == Coordinator and ctx.state.stage == Working and ctx.messages.preparedIn() == PARTICIPANTS)
      Set({ effects: Set(choreo::Broadcast(CoordinatorCommit)), post_state: { ...ctx.state, stage: Committed } })
    else Set()

  pure def decides_on_abort(ctx: LocalContext): Set[Transition] =
    if (ctx.state.role == Coordinator and ctx.state.stage == Working)
      Set({ effects: Set(choreo::Broadcast(CoordinatorAbort)), post_state: { ...ctx.state, stage: Aborted } })
    else Set()

  pure def initialize(n: Node): LocalState =
    { process_id: n, role: if (n == COORDINATOR) Coordinator else Participant, stage: Working }

  action init = choreo::init({
    system: NODES.mapBy(n => initialize(n)),
    messages: NODES.mapBy(n => Set()),
    events: NODES.mapBy(n => Set()),
    extensions: ()
  })

  // --- One named action per transition: what a recorded step calls.
  action step_with(v: Node, listener: LocalContext => Set[Transition]): bool =
    choreo::step_with(v, listener, (c, _) => c)

  action prepare(node: Node): bool = all { PARTICIPANTS.contains(node), step_with(node, spontaneously_prepares) }
  action abort(node: Node): bool = all { PARTICIPANTS.contains(node), step_with(node, spontaneously_aborts) }
  action abortAsInstructed(node: Node): bool = all { PARTICIPANTS.contains(node), step_with(node, aborts_as_instructed) }
  action commitAsInstructed(node: Node): bool = all { PARTICIPANTS.contains(node), step_with(node, commits_as_instructed) }
  action decideCommit: bool = step_with(COORDINATOR, decides_on_commit)
  action decideAbort: bool = step_with(COORDINATOR, decides_on_abort)

  action step = {
    nondet node = PARTICIPANTS.oneOf()
    any {
      prepare(node),
      abort(node),
      abortAsInstructed(node),
      commitAsInstructed(node),
      decideCommit,
      decideAbort,
    }
  }

  // --- Properties
  pure def stageOf(g: choreo::GlobalContext[Node, StateFields, Message, Event, Extensions], n: Node): Stage = g.system.get(n).stage

  // The stage of node n now: what bindings and tests read (choreo's state
  // is not visible outside this module).
  def stage(n: Node): Stage = stageOf(choreo::s, n)

  // No participant commits while another aborts, and none commits unless
  // the coordinator did.
  val consistency = PARTICIPANTS.forall(p => PARTICIPANTS.forall(q =>
    not(stageOf(choreo::s, p) == Committed and stageOf(choreo::s, q) == Aborted)))
  val commitIsDecided = PARTICIPANTS.forall(p =>
    stageOf(choreo::s, p) == Committed implies stageOf(choreo::s, COORDINATOR) == Committed)

  // Witnesses: a run that commits everywhere, and one that aborts.
  val allCommit = not(NODES.forall(n => stageOf(choreo::s, n) == Committed))
  val someAbort = not(stageOf(choreo::s, COORDINATOR) == Aborted)
}

module twophase3 {
  import twophase(PARTICIPANTS = Set("p1", "p2", "p3")).*

  run commitTest = init
    .then(prepare("p1")).then(prepare("p2")).then(prepare("p3"))
    .then(decideCommit)
    .then(commitAsInstructed("p1")).then(commitAsInstructed("p2")).then(commitAsInstructed("p3"))
    .expect(PARTICIPANTS.forall(p => stage(p) == Committed))

  // The coordinator cannot commit before every participant has prepared.
  run earlyCommitTest = init.then(prepare("p1")).then(decideCommit).fail()
}
