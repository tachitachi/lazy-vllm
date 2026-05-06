import express, { Request, Response } from 'express';
import cors from 'cors';
import fs from 'fs-extra';
import path from 'path';
import dotenv from 'dotenv';

dotenv.config();

const app = express();
app.use(cors());
app.use(express.json());

const SYNTHETIC_DIR = process.env.SYNTHETIC_DIR || path.join(process.cwd(), '../../router/data/synthetic');
const ARCHIVE_DIR = process.env.ARCHIVE_DIR || path.join(process.cwd(), '../../router/data/synthetic_archive');
const APPROVED_DIR = process.env.APPROVED_DIR || path.join(process.cwd(), '../../router/data/synthetic_approved');

interface Message {
  role: 'user' | 'assistant';
  content: string;
}

interface Thread {
  thread_id: string;
  history: Message[];
  labels: string[];
}

// Ensure directories exist
fs.ensureDirSync(SYNTHETIC_DIR);
fs.ensureDirSync(ARCHIVE_DIR);
fs.ensureDirSync(APPROVED_DIR);

app.get('/api/threads', async (_req: Request, res: Response) => {
  try {
    const files = await fs.readdir(SYNTHETIC_DIR);
    const jsonFiles = files.filter(file => file.endsWith('.json'));
    res.json(jsonFiles);
  } catch (error) {
    console.error('Error reading threads directory:', error);
    res.status(500).send('Error reading threads');
  }
});

app.get('/api/threads/:id', async (req: Request, res: Response) => {
  try {
    const filePath = path.join(SYNTHETIC_DIR, req.params.id);
    const content = await fs.readJson(filePath);
    res.json(content);
  } catch (error) {
    console.error('Error reading thread:', error);
    res.status(404).send('Thread not found');
  }
});

app.post('/api/threads/:id/label', async (req: Request, res: Response) => {
  try {
    const { labels } = req.body;
    if (!Array.isArray(labels)) {
      return res.status(400).send('Labels must be an array');
    }

    const filePath = path.join(SYNTHETIC_DIR, req.params.id);
    const thread: Thread = await fs.readJson(filePath);

    // Basic validation: ensure labels length matches history length (or at least doesn't exceed it)
    // The user's data seems to have one label per message.
    if (labels.length !== thread.labels.length) {
      return res.status(400).send('Labels length must match history length');
    }

    thread.labels = labels;
    await fs.writeJson(filePath, thread, { spaces: 2 });
    res.json({ message: 'Labels updated successfully' });
  } catch (error) {
    console.error('Error updating labels:', error);
    res.status(500).send('Error updating labels');
  }
});

app.post('/api/threads/:id/archive', async (req: Request, res: Response) => {
  try {
    const oldPath = path.join(SYNTHETIC_DIR, req.params.id);
    const newPath = path.join(ARCHIVE_DIR, req.params.id);

    await fs.move(oldPath, newPath, { overwrite: true });
    res.json({ message: 'Thread archived successfully' });
  } catch (error) {
    console.error('Error archiving thread:', error);
    res.status(500).send('Error archiving thread');
  }
});

app.post('/api/threads/:id/approve', async (req: Request, res: Response) => {
  try {
    const oldPath = path.join(SYNTHETIC_DIR, req.params.id);
    const newPath = path.join(APPROVED_DIR, req.params.id);

    await fs.move(oldPath, newPath, { overwrite: true });
    res.json({ message: 'Thread approved successfully' });
  } catch (error) {
    console.error('Error approving thread:', error);
    res.status(500).send('Error approving thread');
  }
});

const PORT = process.env.PORT || 3003;
app.listen(PORT, () => {
  console.log(`Server running on port ${PORT}`);
  console.log(`Synthetic directory: ${SYNTHETIC_DIR}`);
  console.log(`Archive directory: ${ARCHIVE_DIR}`);
  console.log(`Approved directory: ${APPROVED_DIR}`);
});
