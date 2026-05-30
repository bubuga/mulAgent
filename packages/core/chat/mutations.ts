import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import { chatKeys } from "./queries";
import { createLogger } from "../logger";
import type { ChatSession } from "../types";

const logger = createLogger("chat.mut");

export function useCreateChatSession() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();

  return useMutation({
    mutationFn: (data: {
      kind?: "direct" | "group";
      agent_id?: string;
      agent_ids?: string[];
      orchestrator_agent_id?: string;
      title?: string;
    }) => {
      logger.info("createChatSession.start", { kind: data.kind, titleLength: data.title?.length ?? 0 });
      return api.createChatSession(data);
    },
    onSuccess: (session) => {
      logger.info("createChatSession.success", { sessionId: session.id, agentId: session.agent_id });
    },
    onError: (err) => {
      logger.error("createChatSession.error", err);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
      qc.invalidateQueries({ queryKey: chatKeys.imSessions(wsId) });
    },
  });
}

/**
 * Clears the session's unread state server-side. Optimistically flips
 * has_unread to false in the cached list so the FAB badge drops
 * immediately. The server broadcasts chat:session_read so other devices
 * also sync.
 */
export function useMarkChatSessionRead() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();

  return useMutation({
    mutationFn: (sessionId: string) => {
      logger.info("markChatSessionRead.start", { sessionId });
      return api.markChatSessionRead(sessionId);
    },
    onMutate: async (sessionId) => {
      await qc.cancelQueries({ queryKey: chatKeys.sessions(wsId) });
      await qc.cancelQueries({ queryKey: chatKeys.imSessions(wsId) });

      const prevSessions = qc.getQueryData<ChatSession[]>(chatKeys.sessions(wsId));
      const prevImSessions = qc.getQueryData<ChatSession[]>(chatKeys.imSessions(wsId));

      const clear = (old?: ChatSession[]) =>
        old?.map((s) => (s.id === sessionId ? { ...s, has_unread: false } : s));
      qc.setQueryData<ChatSession[]>(chatKeys.sessions(wsId), clear);
      qc.setQueryData<ChatSession[]>(chatKeys.imSessions(wsId), clear);

      return { prevSessions, prevImSessions };
    },
    onError: (err, sessionId, ctx) => {
      logger.error("markChatSessionRead.error.rollback", { sessionId, err });
      if (ctx?.prevSessions) qc.setQueryData(chatKeys.sessions(wsId), ctx.prevSessions);
      if (ctx?.prevImSessions) qc.setQueryData(chatKeys.imSessions(wsId), ctx.prevImSessions);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
      qc.invalidateQueries({ queryKey: chatKeys.imSessions(wsId) });
    },
  });
}

/**
 * Renames a chat session. Optimistically swaps the title in the cached
 * list so the dropdown reflects the new label immediately; rolls back on
 * error. The matching `chat:session_updated` WS event keeps other
 * tabs/devices in sync — see use-realtime-sync.ts.
 */
export function useUpdateChatSession() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();

  return useMutation({
    mutationFn: (data: { sessionId: string; title: string }) => {
      logger.info("updateChatSession.start", {
        sessionId: data.sessionId,
        titleLength: data.title.length,
      });
      return api.updateChatSession(data.sessionId, { title: data.title });
    },
    onMutate: async ({ sessionId, title }) => {
      await qc.cancelQueries({ queryKey: chatKeys.sessions(wsId) });

      const prevSessions = qc.getQueryData<ChatSession[]>(chatKeys.sessions(wsId));

      const patch = (old?: ChatSession[]) =>
        old?.map((s) => (s.id === sessionId ? { ...s, title } : s));
      qc.setQueryData<ChatSession[]>(chatKeys.sessions(wsId), patch);

      return { prevSessions };
    },
    onError: (err, vars, ctx) => {
      logger.error("updateChatSession.error.rollback", { sessionId: vars.sessionId, err });
      if (ctx?.prevSessions) qc.setQueryData(chatKeys.sessions(wsId), ctx.prevSessions);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
      qc.invalidateQueries({ queryKey: chatKeys.imSessions(wsId) });
    },
  });
}

/**
 * Hard-deletes a chat session. Optimistically removes the row from the
 * sessions list so the dropdown updates instantly; rolls back on error.
 * The matching `chat:session_deleted` WS event keeps other tabs/devices
 * in sync — see use-realtime-sync.ts.
 */
export function useDeleteChatSession() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();

  return useMutation({
    mutationFn: (sessionId: string) => {
      logger.info("deleteChatSession.start", { sessionId });
      return api.deleteChatSession(sessionId);
    },
    onMutate: async (sessionId) => {
      await qc.cancelQueries({ queryKey: chatKeys.sessions(wsId) });

      const prevSessions = qc.getQueryData<ChatSession[]>(chatKeys.sessions(wsId));

      const drop = (old?: ChatSession[]) => old?.filter((s) => s.id !== sessionId);
      qc.setQueryData<ChatSession[]>(chatKeys.sessions(wsId), drop);

      logger.debug("deleteChatSession.optimistic", { sessionId });
      return { prevSessions };
    },
    onError: (err, sessionId, ctx) => {
      logger.error("deleteChatSession.error.rollback", { sessionId, err });
      if (ctx?.prevSessions) qc.setQueryData(chatKeys.sessions(wsId), ctx.prevSessions);
    },
    onSettled: (_data, _err, sessionId) => {
      logger.debug("deleteChatSession.settled", { sessionId });
      qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
      qc.invalidateQueries({ queryKey: chatKeys.imSessions(wsId) });
    },
  });
}

export function usePinChatSession() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (sessionId: string) => api.pinChatSession(sessionId),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
      qc.invalidateQueries({ queryKey: chatKeys.imSessions(wsId) });
    },
  });
}

export function useUnpinChatSession() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (sessionId: string) => api.unpinChatSession(sessionId),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
      qc.invalidateQueries({ queryKey: chatKeys.imSessions(wsId) });
    },
  });
}

export function useArchiveChatSession() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (sessionId: string) => api.archiveChatSession(sessionId),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
      qc.invalidateQueries({ queryKey: chatKeys.imSessions(wsId) });
    },
  });
}

export function useUnarchiveChatSession() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (sessionId: string) => api.unarchiveChatSession(sessionId),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
      qc.invalidateQueries({ queryKey: chatKeys.imSessions(wsId) });
    },
  });
}

// PR7: Step execution mutations

function invalidateStepQueries(qc: ReturnType<typeof useQueryClient>, sessionId: string) {
  qc.invalidateQueries({ queryKey: chatKeys.messages(sessionId) });
  qc.invalidateQueries({ queryKey: chatKeys.pendingTask(sessionId) });
  qc.invalidateQueries({ queryKey: chatKeys.activePlan(sessionId) });
}

export function useContinueStep(sessionId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ stepId, prompt }: { stepId: string; prompt?: string }) =>
      api.continueStep(stepId, prompt),
    onSettled: () => invalidateStepQueries(qc, sessionId),
  });
}

export function useSkipStep(sessionId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (stepId: string) => api.skipStep(stepId),
    onSettled: () => invalidateStepQueries(qc, sessionId),
  });
}

export function useCancelStep(sessionId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (stepId: string) => api.cancelStep(stepId),
    onSettled: () => invalidateStepQueries(qc, sessionId),
  });
}

export function useRetryStep(sessionId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ stepId, prompt }: { stepId: string; prompt?: string }) =>
      api.retryStep(stepId, prompt),
    onSettled: () => invalidateStepQueries(qc, sessionId),
  });
}

export function useRequestReplan(sessionId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (planId: string) => api.requestReplan(planId),
    onSettled: () => invalidateStepQueries(qc, sessionId),
  });
}
