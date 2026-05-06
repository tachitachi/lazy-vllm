import React, { useState, useEffect } from 'react';
import { api } from '../api';
import type { Thread, Message } from '../api';
import { Loader2, Archive, Check, AlertCircle, ChevronRight, ChevronLeft } from 'lucide-react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

interface ChatViewProps {
  threadId: string;
  onArchive: () => void;
  onApprove: () => void;
}

const ChatView: React.FC<ChatViewProps> = ({ threadId, onArchive, onApprove }) => {
  const [thread, setThread] = useState<Thread | null>(null);
  const [loading, setLoading] = useState(true);
  const [updating, setUpdating] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const fetchThread = async () => {
      setLoading(true);
      setError(null);
      try {
        const data = await api.getThread(threadId);
        setThread(data);
      } catch (err) {
        setError('Failed to load thread');
        console.error(err);
      } finally {
        setLoading(false);
      }
    };
    fetchThread();
  }, [threadId]);

  const handleLabelToggle = async (index: number) => {
    if (!thread) return;
    setUpdating(index);
    try {
      const newLabels = [...thread.labels];
      // Toggle between REASONING and DIRECT
      newLabels[index] = newLabels[index] === 'REASONING' ? 'DIRECT' : 'REASONING';

      await api.updateLabel(threadId, newLabels);
      setThread({ ...thread, labels: newLabels });
    } catch (err) {
      setError('Failed to update label');
      console.error(err);
    } finally {
      setUpdating(null);
    }
  };

  if (loading) {
    return (
      <div className="flex items-center justify-center h-full">
        <Loader2 className="animate-spin text-gray-400" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-red-500">
        <AlertCircle size={48} className="mb-4" />
        <p>{error}</p>
      </div>
    );
  }

  if (!thread) {
    return <div className="p-8 text-center text-gray-500">No thread selected.</div>;
  }

  return (
    <div className="flex flex-col h-full bg-white dark:bg-gray-950">
      {/* Header */}
      <div className="flex items-center justify-between p-4 border-b border-gray-200 dark:border-gray-800 bg-white dark:bg-gray-950 sticky top-0 z-10">
        <div className="flex flex-col min-w-0">
          <h2 className="text-sm font-mono truncate text-gray-500 dark:text-gray-400" title={thread.thread_id}>
            {thread.thread_id}
          </h2>
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={onApprove}
            className="flex items-center gap-2 px-3 py-1.5 text-sm font-medium text-green-600 hover:bg-green-50 dark:hover:bg-green-900/20 rounded-md transition-colors"
          >
            <Check size={16} />
            Approve
          </button>
          <button
            onClick={onArchive}
            className="flex items-center gap-2 px-3 py-1.5 text-sm font-medium text-red-600 hover:bg-red-50 dark:hover:bg-red-900/20 rounded-md transition-colors"
          >
            <Archive size={16} />
            Archive
          </button>
        </div>
      </div>

      {/* Messages */}
      <div className="flex-1 overflow-y-auto p-4 space-y-6">
        {thread.history.map((msg, idx) => {
          const isAssistant = msg.role === 'assistant';
          const label = isAssistant ? thread.labels[Math.floor(idx / 2)] : null;

          return (
            <div
              key={idx}
              className={`flex flex-col ${isAssistant ? 'items-start' : 'items-end'}`}
            >
              <div className="flex items-center gap-2 mb-1 text-xs font-medium text-gray-500 uppercase tracking-wider">
                <span>{msg.role}</span>
                {isAssistant && (
                  <div className="flex items-center gap-1">
                    <span className={`px-1.5 py-0.5 rounded text-[10px] font-bold ${
                      label === 'REASONING'
                        ? 'bg-purple-100 text-purple-700 dark:bg-purple-900/40 dark:text-purple-300'
                        : 'bg-blue-100 text-blue-700 dark:bg-blue-900/40 dark:text-blue-300'
                    }`}>
                      {label}
                    </span>
                    <button
                      onClick={() => handleLabelToggle(Math.floor(idx / 2))}
                      disabled={updating === idx}
                      className="p-0.5 hover:bg-gray-200 dark:hover:bg-gray-800 rounded text-gray-400 hover:text-gray-600 dark:hover:text-gray-200 transition-colors disabled:opacity-50"
                      title="Toggle Label"
                    >
                      {updating === idx ? <Loader2 size={12} className="animate-spin" /> : <Check size={12} />}
                    </button>
                  </div>
                )}
              </div>

              <div
                className={`max-w-[85%] p-4 rounded-2xl text-sm leading-relaxed overflow-hidden ${
                  isAssistant
                    ? 'bg-gray-100 text-gray-900 dark:bg-gray-800 dark:text-gray-100 rounded-tl-none prose dark:prose-invert prose-sm max-w-none'
                    : 'bg-blue-600 text-white rounded-tr-none'
                }`}
              >
                {isAssistant ? (
                  <ReactMarkdown remarkPlugins={[remarkGfm]}>
                    {msg.content}
                  </ReactMarkdown>
                ) : (
                  <div className="whitespace-pre-wrap">{msg.content}</div>
                )}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
};

export default ChatView;
