import React from 'react';
import { api } from '../api';
import { Loader2, FileJson } from 'lucide-react';

interface ThreadListProps {
  onSelectThread: (id: string) => void;
  selectedThreadId: string | null;
  isLoading: boolean;
}

const ThreadList: React.FC<ThreadListProps> = ({ onSelectThread, selectedThreadId, isLoading }) => {
  const [threads, setThreads] = React.useState<string[]>([]);
  const [error, setError] = React.useState<string | null>(null);

  React.useEffect(() => {
    const fetchThreads = async () => {
      try {
        const data = await api.getThreads();
        setThreads(data);
      } catch (err) {
        setError('Failed to load threads');
        console.error(err);
      }
    };
    fetchThreads();
  }, []);

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-full">
        <Loader2 className="animate-spin text-gray-400" />
      </div>
    );
  }

  if (error) {
    return <div className="p-4 text-red-500">{error}</div>;
  }

  return (
    <div className="flex flex-col h-full border-r border-gray-200 dark:border-gray-800 bg-gray-50 dark:bg-gray-900">
      <div className="p-4 border-b border-gray-200 dark:border-gray-800">
        <h2 className="text-lg font-semibold">Threads</h2>
      </div>
      <div className="flex-1 overflow-y-auto">
        {threads.length === 0 ? (
          <div className="p-4 text-gray-500">No threads found.</div>
        ) : (
          <ul className="divide-y divide-gray-200 dark:divide-gray-800">
            {threads.map((id) => (
              <li
                key={id}
                onClick={() => onSelectThread(id)}
                className={`p-3 cursor-pointer flex items-center gap-3 hover:bg-gray-100 dark:hover:bg-gray-800 transition-colors ${
                  selectedThreadId === id ? 'bg-blue-50 dark:bg-blue-900/20 text-blue-600 dark:text-blue-400' : ''
                }`}
              >
                <FileJson size={18} className="text-gray-400" />
                <span className="text-sm truncate">{id}</span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
};

export default ThreadList;
